// Package host is the host agent's HTTP API. The central server creates and
// destroys sandboxes here; the CLI sends files and runs commands here
// directly, with the sandbox's own token, so neither passes through the
// central server.
package host

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
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

	mu        sync.Mutex
	sandboxes map[string]*entry
}

type entry struct {
	token   string
	created time.Time
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
	mux.HandleFunc("POST /v1/sandboxes/{id}/exec", s.exec)
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
	s.mu.Unlock()
	switch {
	case allowSecret && equal(tok, s.Secret):
		return id, true
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
	s.sandboxes[req.ID] = &entry{token: req.Token, created: time.Now()}
	s.mu.Unlock()

	if err := s.Backend.Create(r.Context(), req.ID, req.Spec); err != nil {
		s.forget(req.ID)
		s.Backend.Destroy(context.WithoutCancel(r.Context()), req.ID)
		s.log().Error("create failed", "sandbox", req.ID, "err", err)
		WriteError(w, http.StatusInternalServerError, "create sandbox: %v", err)
		return
	}
	s.log().Info("sandbox created", "sandbox", req.ID, "image", req.Spec.Image, "network", req.Spec.Network)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sandboxAuth(w, r, false)
	if !ok {
		return
	}
	pr, pw := io.Pipe()
	sanitized := make(chan error, 1)
	go func() {
		err := worktree.Sanitize(pw, r.Body, MaxUpload)
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
	w.Header().Set("Content-Type", stream.ContentType)
	w.WriteHeader(http.StatusOK)
	sw := stream.NewWriter(w)
	code, err := s.Backend.Exec(r.Context(), id, req, sw.To(stream.Stdout), sw.To(stream.Stderr))
	if err != nil {
		sw.Fail(err.Error())
		return
	}
	sw.Exit(code)
}

func (s *Server) destroy(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sandboxAuth(w, r, true)
	if !ok {
		return
	}
	if err := s.Backend.Destroy(r.Context(), id); err != nil {
		s.log().Error("destroy failed", "sandbox", id, "err", err)
		WriteError(w, http.StatusInternalServerError, "destroy: %v", err)
		return
	}
	s.forget(id)
	s.log().Info("sandbox destroyed", "sandbox", id)
	w.WriteHeader(http.StatusNoContent)
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
// interval as a heartbeat until ctx is done.
func Register(ctx context.Context, client *http.Client, serverURL, secret string, req api.RegisterHostRequest, interval time.Duration, log *slog.Logger) {
	body, _ := json.Marshal(req)
	send := func() error {
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/v1/hosts", strings.NewReader(string(body)))
		if err != nil {
			return err
		}
		hr.Header.Set("Authorization", "Bearer "+secret)
		hr.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(hr)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
		}
		return nil
	}
	registered := false
	for {
		err := send()
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("registration failed", "server", serverURL, "err", err)
			registered = false
		case err == nil && !registered:
			log.Info("registered with central server", "server", serverURL, "name", req.Name)
			registered = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
