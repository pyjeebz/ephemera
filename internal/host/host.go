// Package host is the host agent's HTTP API. The central server creates and
// destroys sandboxes here; the CLI sends files and runs commands here
// directly, with the sandbox's own token, so neither passes through the
// central server.
package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/egress"
	"github.com/pyjeebz/ephemera/internal/record"
	"github.com/pyjeebz/ephemera/internal/sandbox"
	"github.com/pyjeebz/ephemera/internal/stream"
	"github.com/pyjeebz/ephemera/internal/worktree"
)

// MaxUpload caps the unpacked size of one file upload.
const MaxUpload = 4 << 30

// Server serves the host API.
type Server struct {
	Backend      sandbox.Backend
	Secret       string // shared with the central server
	DefaultImage string
	Log          *slog.Logger
	// Name and BackendName go into records.
	Name        string
	BackendName string
	// Key signs each sandbox's record when it is destroyed.
	Key ed25519.PrivateKey
	// MaxLifetime destroys any sandbox this long after it was created.
	// A sandbox may ask for less, never more. 0 means no limit.
	MaxLifetime time.Duration

	mu        sync.Mutex
	sandboxes map[string]*entry
}

type entry struct {
	token   string
	created time.Time
	rec     api.Record
	expiry  *time.Timer
	// ending is set once the sandbox is being destroyed; final holds the
	// signed record of a sandbox that expired, until it is collected.
	ending bool
	final  *api.SignedRecord
}

// keepExpired is how long the record of an expired sandbox waits to be
// collected by a DELETE.
const keepExpired = time.Hour

// note adds to a sandbox's record.
func (s *Server) note(id string, f func(*api.Record)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.sandboxes[id]; ok && !e.ending {
		f(&e.rec)
	}
}

// Live returns the number of sandboxes on this host.
func (s *Server) Live() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sandboxes)
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /v1/sandboxes", s.create)
	mux.HandleFunc("PUT /v1/sandboxes/{id}/files", s.files)
	mux.HandleFunc("POST /v1/sandboxes/{id}/files/delete", s.deleteFiles)
	mux.HandleFunc("POST /v1/sandboxes/{id}/exec", s.exec)
	mux.HandleFunc("GET /v1/sandboxes/{id}/ports/{port}", s.port)
	mux.HandleFunc("GET /v1/sandboxes/{id}/connections", s.connections)
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", s.destroy)
	return mux
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return t
	}
	return ""
}

func equal(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// WriteError writes a JSON error response.
func WriteError(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(api.Error{Error: fmt.Sprintf(format, args...)})
}

// sandboxAuth checks the request's token against the sandbox's own token,
// or the central server's secret when allowSecret is set.
func (s *Server) sandboxAuth(w http.ResponseWriter, r *http.Request, allowSecret bool) (string, bool) {
	id := r.PathValue("id")
	tok := bearer(r)
	s.mu.Lock()
	e, ok := s.sandboxes[id]
	expired := ok && e.final != nil
	s.mu.Unlock()
	switch {
	case allowSecret && equal(tok, s.Secret):
		return id, true
	case ok && equal(tok, e.token) && expired && !allowSecret:
		WriteError(w, http.StatusGone, "sandbox %s reached its maximum lifetime and was destroyed", id)
	case ok && equal(tok, e.token):
		return id, true
	case !ok:
		WriteError(w, http.StatusNotFound, "sandbox %s not found", id)
	default:
		WriteError(w, http.StatusUnauthorized, "invalid token")
	}
	return "", false
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	if !equal(bearer(r), s.Secret) {
		WriteError(w, http.StatusUnauthorized, "invalid host secret")
		return
	}
	var req api.HostCreateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	if req.ID == "" || req.Token == "" {
		WriteError(w, http.StatusBadRequest, "id and token are required")
		return
	}
	if err := req.Spec.Validate(); err != nil {
		WriteError(w, http.StatusBadRequest, "%v", err)
		return
	}
	for _, pat := range req.Spec.Allow {
		if err := egress.ValidatePattern(pat); err != nil {
			WriteError(w, http.StatusBadRequest, "%v", err)
			return
		}
	}
	if req.Spec.Image == "" {
		req.Spec.Image = s.DefaultImage
	}
	s.mu.Lock()
	if s.sandboxes == nil {
		s.sandboxes = map[string]*entry{}
	}
	if _, dup := s.sandboxes[req.ID]; dup {
		s.mu.Unlock()
		WriteError(w, http.StatusConflict, "sandbox %s already exists", req.ID)
		return
	}
	now := time.Now()
	s.sandboxes[req.ID] = &entry{token: req.Token, created: now, rec: api.Record{
		Version: 1, Sandbox: req.ID, Host: s.Name, Backend: s.BackendName, Spec: req.Spec, Created: now.UTC(),
		Execs: []api.ExecRecord{}, Syncs: []api.SyncRecord{}, Connections: []api.Connection{},
	}}
	s.mu.Unlock()

	if err := s.Backend.Create(r.Context(), req.ID, req.Spec); err != nil {
		s.forget(req.ID)
		s.Backend.Destroy(context.WithoutCancel(r.Context()), req.ID)
		s.log().Error("create failed", "sandbox", req.ID, "err", err)
		WriteError(w, http.StatusInternalServerError, "create sandbox: %v", err)
		return
	}
	if life := s.lifetime(req.Spec); life > 0 {
		id := req.ID
		t := time.AfterFunc(life, func() { s.expire(id) })
		s.mu.Lock()
		if e, ok := s.sandboxes[id]; ok {
			e.expiry = t
		}
		s.mu.Unlock()
	}
	s.log().Info("sandbox created", "sandbox", req.ID, "image", req.Spec.Image, "network", req.Spec.Network)
	w.WriteHeader(http.StatusCreated)
}

// lifetime is how long a sandbox may live: the host's limit, or less if
// the sandbox asked for less.
func (s *Server) lifetime(spec api.Spec) time.Duration {
	life := s.MaxLifetime
	if asked := time.Duration(spec.MaxLifetimeSeconds) * time.Second; asked > 0 && (life == 0 || asked < life) {
		life = asked
	}
	return life
}

func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sandboxAuth(w, r, false)
	if !ok {
		return
	}
	pr, pw := io.Pipe()
	sanitized := make(chan error, 1)
	var nfiles int
	var nbytes int64
	go func() {
		var err error
		nfiles, nbytes, err = worktree.SanitizeCount(pw, r.Body, MaxUpload)
		pw.CloseWithError(err)
		sanitized <- err
	}()
	err := s.Backend.CopyIn(r.Context(), id, pr)
	pr.CloseWithError(err) // unblocks Sanitize if CopyIn stopped reading early
	if serr := <-sanitized; serr != nil && !errors.Is(serr, io.ErrClosedPipe) {
		err = serr // the real reason: the upload was refused
	}
	if err != nil {
		WriteError(w, http.StatusBadRequest, "upload: %v", err)
		return
	}
	s.note(id, func(rec *api.Record) {
		rec.Syncs = append(rec.Syncs, api.SyncRecord{Time: time.Now().UTC(), Files: nfiles, Bytes: nbytes})
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sandboxAuth(w, r, false)
	if !ok {
		return
	}
	var req api.ExecRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	if len(req.Argv) == 0 {
		WriteError(w, http.StatusBadRequest, "argv is required")
		return
	}
	if req.TimeoutSeconds < 0 {
		WriteError(w, http.StatusBadRequest, "timeout cannot be negative")
		return
	}
	w.Header().Set("Content-Type", stream.ContentType)
	w.WriteHeader(http.StatusOK)
	sw := stream.NewWriter(w)
	ctx := r.Context()
	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	started := time.Now()
	code, err := s.Backend.Exec(ctx, id, req, sw.To(stream.Stdout), sw.To(stream.Stderr))
	er := api.ExecRecord{Argv: req.Argv, Started: started.UTC(), DurationMS: time.Since(started).Milliseconds(), ExitCode: code}
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Context().Err() == nil {
		// The command's own timeout, not the client going away.
		msg := fmt.Sprintf("ephemera: the command timed out after %ds and was stopped\n", req.TimeoutSeconds)
		sw.Frame(stream.Stderr, []byte(msg))
		er.ExitCode, er.Error = api.ExitTimedOut, "timed out"
		s.note(id, func(rec *api.Record) { rec.Execs = append(rec.Execs, er) })
		sw.Exit(api.ExitTimedOut)
		return
	}
	if err != nil {
		er.Error = err.Error()
		s.note(id, func(rec *api.Record) { rec.Execs = append(rec.Execs, er) })
		sw.Fail(err.Error())
		return
	}
	s.note(id, func(rec *api.Record) { rec.Execs = append(rec.Execs, er) })
	sw.Exit(code)
}

func (s *Server) deleteFiles(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sandboxAuth(w, r, false)
	if !ok {
		return
	}
	var req api.DeleteFilesRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	for _, p := range req.Paths {
		if err := worktree.CheckPath(p); err != nil {
			WriteError(w, http.StatusBadRequest, "%v", err)
			return
		}
	}
	const batch = 200
	for len(req.Paths) > 0 {
		n := min(len(req.Paths), batch)
		var out strings.Builder
		argv := append([]string{"rm", "-f", "--"}, req.Paths[:n]...)
		code, err := s.Backend.Exec(r.Context(), id, api.ExecRequest{Argv: argv}, &out, &out)
		if err == nil && code != 0 {
			err = fmt.Errorf("rm exited %d: %s", code, strings.TrimSpace(out.String()))
		}
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "delete: %v", err)
			return
		}
		s.note(id, func(rec *api.Record) {
			rec.Syncs = append(rec.Syncs, api.SyncRecord{Time: time.Now().UTC(), Deleted: n})
		})
		req.Paths = req.Paths[n:]
	}
	w.WriteHeader(http.StatusNoContent)
}

// port connects the client to a TCP port inside the sandbox. The request is
// an HTTP Upgrade; after the 101 response the connection carries raw bytes
// both ways.
func (s *Server) port(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sandboxAuth(w, r, false)
	if !ok {
		return
	}
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 {
		WriteError(w, http.StatusBadRequest, "invalid port")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), api.PortUpgrade) {
		WriteError(w, http.StatusUpgradeRequired, "Upgrade: %s required", api.PortUpgrade)
		return
	}
	dctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	upstream, err := s.Backend.Dial(dctx, id, port)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "nothing is listening on port %d in the sandbox: %v", port, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		WriteError(w, http.StatusInternalServerError, "connection cannot be upgraded")
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: %s\r\n\r\n", api.PortUpgrade)
	if err := buf.Flush(); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	// Bytes the client sent right after the request are already buffered.
	if n := buf.Reader.Buffered(); n > 0 {
		pending, _ := buf.Reader.Peek(n)
		if _, err := upstream.Write(pending); err != nil {
			client.Close()
			upstream.Close()
			return
		}
	}
	Pipe(client, upstream)
}

// Pipe copies between two connections until both directions are done,
// passing on half-closes, then closes both.
func Pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// connections returns the sandbox's outbound connection log, from the
// "since" index on.
func (s *Server) connections(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sandboxAuth(w, r, false)
	if !ok {
		return
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	out := []api.Connection{}
	if nl, ok := s.Backend.(sandbox.NetworkLogger); ok {
		var err error
		if out, err = nl.Connections(id, since); err != nil {
			WriteError(w, http.StatusNotFound, "%v", err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// destroy destroys the sandbox and answers with its signed record. For a
// sandbox that already expired, it hands over the record kept for it.
func (s *Server) destroy(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sandboxAuth(w, r, true)
	if !ok {
		return
	}
	s.mu.Lock()
	e := s.sandboxes[id]
	if e != nil && e.final != nil {
		final := e.final
		delete(s.sandboxes, id)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, final)
		return
	}
	s.mu.Unlock()
	sr, err := s.finish(r.Context(), id, "destroyed")
	if err != nil {
		s.log().Error("destroy failed", "sandbox", id, "err", err)
		WriteError(w, http.StatusInternalServerError, "destroy: %v", err)
		return
	}
	s.forget(id)
	s.log().Info("sandbox destroyed", "sandbox", id)
	writeJSON(w, http.StatusOK, sr)
}

// finish destroys a sandbox and returns its signed record. Only the first
// caller does the work; the record is complete once the backend confirms
// everything is gone.
func (s *Server) finish(ctx context.Context, id, how string) (*api.SignedRecord, error) {
	s.mu.Lock()
	e, ok := s.sandboxes[id]
	if !ok || e.ending {
		s.mu.Unlock()
		return nil, fmt.Errorf("sandbox %s is already being destroyed", id)
	}
	e.ending = true
	if e.expiry != nil {
		e.expiry.Stop()
	}
	rec := e.rec
	s.mu.Unlock()

	// The connection log lives with the sandbox: read it first.
	if nl, ok := s.Backend.(sandbox.NetworkLogger); ok {
		if conns, err := nl.Connections(id, 0); err == nil {
			rec.Connections = conns
		}
	}
	if err := s.Backend.Destroy(ctx, id); err != nil {
		s.mu.Lock()
		e.ending = false
		s.mu.Unlock()
		return nil, err
	}
	rec.Destroyed, rec.Ended, rec.DiskDeleted = time.Now().UTC(), how, true
	if s.Key == nil {
		return nil, errors.New("host has no signing key")
	}
	sr, err := record.Sign(s.Key, rec)
	if err != nil {
		return nil, err
	}
	return &sr, nil
}

// expire destroys a sandbox that reached its maximum lifetime, and keeps
// its record for a while so the CLI can still collect it.
func (s *Server) expire(id string) {
	sr, err := s.finish(context.Background(), id, "expired")
	if err != nil {
		s.log().Error("expire", "sandbox", id, "err", err)
		return
	}
	s.mu.Lock()
	if e, ok := s.sandboxes[id]; ok {
		e.final = sr
	}
	s.mu.Unlock()
	s.log().Info("sandbox expired", "sandbox", id)
	time.AfterFunc(keepExpired, func() { s.forget(id) })
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) forget(id string) {
	s.mu.Lock()
	delete(s.sandboxes, id)
	s.mu.Unlock()
}

// DestroyAll destroys every sandbox on the host, for shutdown: nothing is
// left behind when the host agent stops.
func (s *Server) DestroyAll(ctx context.Context) error {
	s.mu.Lock()
	ids := make([]string, 0, len(s.sandboxes))
	for id := range s.sandboxes {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	var errs []error
	for _, id := range ids {
		if err := s.Backend.Destroy(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		s.forget(id)
	}
	return errors.Join(errs...)
}

// Register announces the host to the central server, then repeats every
// interval as a heartbeat until ctx is done. Each heartbeat lists this
// host's sandboxes; any the central server has no record of are destroyed.
func (s *Server) Register(ctx context.Context, client *http.Client, serverURL, secret string, req api.RegisterHostRequest, interval time.Duration) {
	send := func() (api.RegisterHostResponse, error) {
		var out api.RegisterHostResponse
		req.Sandboxes = s.ids()
		if s.Key != nil {
			req.PublicKey = record.PublicKey(s.Key)
		}
		body, _ := json.Marshal(req)
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/v1/hosts", bytes.NewReader(body))
		if err != nil {
			return out, err
		}
		hr.Header.Set("Authorization", "Bearer "+secret)
		hr.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(hr)
		if err != nil {
			return out, err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return out, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
		}
		if resp.StatusCode == http.StatusOK {
			err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
		}
		return out, err
	}
	registered := false
	for {
		resp, err := send()
		switch {
		case err != nil && ctx.Err() == nil:
			s.log().Warn("registration failed", "server", serverURL, "err", err)
			registered = false
		case err == nil && !registered:
			s.log().Info("registered with central server", "server", serverURL, "name", req.Name)
			registered = true
		}
		for _, id := range resp.Destroy {
			s.log().Warn("central server has no record of sandbox; destroying it", "sandbox", id)
			if err := s.Backend.Destroy(ctx, id); err != nil {
				s.log().Error("destroy unknown sandbox", "sandbox", id, "err", err)
				continue
			}
			s.forget(id)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (s *Server) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.sandboxes))
	for id := range s.sandboxes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
