// Package control is the central server. It knows which hosts exist and
// which sandboxes run where. It handles ids and states only: files, command
// output and secrets go straight between the CLI and the host.
//
// The sandbox list is saved to StatePath so a restart keeps track of
// running sandboxes. Every host heartbeat lists what the host really runs:
// records the host no longer has are dropped, and sandboxes the central
// server does not know about are destroyed.
package control

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/host"
)

// HostTimeout is how long a host stays eligible after its last heartbeat.
const HostTimeout = 45 * time.Second

// Server serves the central API.
type Server struct {
	Tokens     Tokens // API tokens for CLI requests
	HostSecret string // shared with every host
	StatePath  string // where the sandbox list is saved; empty keeps it in memory
	Client     *http.Client
	Log        *slog.Logger
	Now        func() time.Time

	mu        sync.Mutex
	hosts     map[string]*hostState
	sandboxes map[string]*api.Sandbox
	// usage is finished sandboxes' seconds, by owner and UTC day.
	usage map[string]map[string]float64
	// limits is each owner's limits as last seen on a request.
	limits map[string]Limits

	saveMu sync.Mutex
}

// createGrace is how long a new sandbox may be missing from its host's
// heartbeat before its record is dropped: the host may still be creating it.
const createGrace = 2 * time.Minute

type stateFile struct {
	Sandboxes []api.Sandbox                 `json:"sandboxes"`
	Usage     map[string]map[string]float64 `json:"usage,omitempty"`
}

// Load reads the saved sandbox list, if there is one.
func (s *Server) Load() error {
	if s.StatePath == "" {
		return nil
	}
	b, err := os.ReadFile(s.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var st stateFile
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("%s: %w", s.StatePath, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = st.Usage
	s.sandboxes = make(map[string]*api.Sandbox, len(st.Sandboxes))
	for i := range st.Sandboxes {
		sb := st.Sandboxes[i]
		sb.Token = ""
		s.sandboxes[sb.ID] = &sb
	}
	return nil
}

// save writes the sandbox list. Sandbox tokens are never written.
func (s *Server) save() {
	if s.StatePath == "" {
		return
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	st := stateFile{Sandboxes: make([]api.Sandbox, 0, len(s.sandboxes)), Usage: map[string]map[string]float64{}}
	cutoff := s.now().UTC().AddDate(0, 0, -31).Format(dayFormat) // keep a month
	for owner, days := range s.usage {
		for day, secs := range days {
			if day >= cutoff {
				if st.Usage[owner] == nil {
					st.Usage[owner] = map[string]float64{}
				}
				st.Usage[owner][day] = secs
			}
		}
	}
	for _, sb := range s.sandboxes {
		c := *sb
		c.Token = ""
		st.Sandboxes = append(st.Sandboxes, c)
	}
	s.mu.Unlock()
	sort.Slice(st.Sandboxes, func(i, j int) bool { return st.Sandboxes[i].ID < st.Sandboxes[j].ID })
	err := os.MkdirAll(filepath.Dir(s.StatePath), 0o700)
	if err == nil {
		err = writeJSONAtomic(s.StatePath, st, 0o600)
	}
	if err != nil {
		s.log().Error("save state", "path", s.StatePath, "err", err)
	}
}

type hostState struct {
	api.RegisterHostRequest
	lastSeen time.Time
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /v1/hosts", s.register)
	mux.HandleFunc("GET /v1/whoami", s.user(s.whoami))
	mux.HandleFunc("GET /v1/usage", s.user(s.usageHandler))
	mux.HandleFunc("GET /v1/hosts", s.user(s.hostList))
	mux.HandleFunc("POST /v1/sandboxes", s.user(s.create))
	mux.HandleFunc("GET /v1/sandboxes", s.user(s.list))
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", s.user(s.destroy))
	return mux
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Server) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func tokenOK(r *http.Request, want string) bool {
	got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

type ctxKey struct{}

func (s *Server) user(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var p Principal
		ok := false
		if s.Tokens != nil && tok != "" {
			p, ok = s.Tokens.Check(tok)
		}
		if !ok {
			host.WriteError(w, http.StatusUnauthorized, "invalid token; run ephemera login")
			return
		}
		s.mu.Lock()
		if s.limits == nil {
			s.limits = map[string]Limits{}
		}
		s.limits[p.Name] = p.Limits
		s.mu.Unlock()
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	}
}

func principal(r *http.Request) Principal {
	p, _ := r.Context().Value(ctxKey{}).(Principal)
	return p
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.WhoAmI{Name: principal(r).Name})
}

const dayFormat = "2006-01-02"

// accrue adds a finished sandbox's time to its owner's usage, split by UTC
// day. Locked.
func (s *Server) accrue(sb *api.Sandbox, end time.Time) {
	if sb.Owner == "" {
		return
	}
	if s.usage == nil {
		s.usage = map[string]map[string]float64{}
	}
	if s.usage[sb.Owner] == nil {
		s.usage[sb.Owner] = map[string]float64{}
	}
	for start := sb.CreatedAt.UTC(); start.Before(end); {
		dayEnd := time.Date(start.Year(), start.Month(), start.Day()+1, 0, 0, 0, 0, time.UTC)
		stop := end
		if dayEnd.Before(stop) {
			stop = dayEnd
		}
		s.usage[sb.Owner][start.Format(dayFormat)] += stop.Sub(start).Seconds()
		start = stop
	}
}

// usedToday is an owner's sandbox-seconds today, running ones included,
// and how many are running. Locked.
func (s *Server) usedToday(owner string) (float64, int) {
	now := s.now().UTC()
	today := now.Format(dayFormat)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	used := s.usage[owner][today]
	running := 0
	for _, sb := range s.sandboxes {
		if sb.Owner != owner {
			continue
		}
		running++
		start := sb.CreatedAt
		if start.Before(midnight) {
			start = midnight
		}
		if now.After(start) {
			used += now.Sub(start).Seconds()
		}
	}
	return used, running
}

// admit checks a new sandbox against the owner's limits, and caps its
// lifetime at the minutes left today.
func (s *Server) admit(p Principal, spec *api.Spec) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	used, running := s.usedToday(p.Name)
	if p.MaxSandboxes > 0 && running >= p.MaxSandboxes {
		return fmt.Sprintf("limit reached: %s may run %d sandboxes at once; destroy one first", p.Name, p.MaxSandboxes), false
	}
	if p.MaxMinutesPerDay > 0 {
		left := int(float64(p.MaxMinutesPerDay*60) - used)
		if left <= 0 {
			return fmt.Sprintf("limit reached: %s has used today's %d sandbox-minutes; it resets at midnight UTC", p.Name, p.MaxMinutesPerDay), false
		}
		if spec.MaxLifetimeSeconds == 0 || spec.MaxLifetimeSeconds > left {
			spec.MaxLifetimeSeconds = left
		}
	}
	return "", true
}

func (s *Server) usageOf(p Principal) api.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	used, running := s.usedToday(p.Name)
	return api.Usage{Name: p.Name, Day: s.now().UTC().Format(dayFormat), MinutesToday: used / 60,
		MaxMinutesPerDay: p.MaxMinutesPerDay, Running: running, MaxSandboxes: p.MaxSandboxes}
}

func (s *Server) usageHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.usageOf(principal(r)))
}

// Enforce stops sandboxes whose owner has used up today's minutes, every
// interval until ctx is done. Creating sandboxes is refused at the limit
// either way; this catches ones already running.
func (s *Server) Enforce(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.enforceOnce(ctx)
	}
}

func (s *Server) enforceOnce(ctx context.Context) {
	s.mu.Lock()
	var over []*api.Sandbox
	for owner, l := range s.limits {
		if l.MaxMinutesPerDay <= 0 {
			continue
		}
		if used, _ := s.usedToday(owner); used >= float64(l.MaxMinutesPerDay*60) {
			for _, sb := range s.sandboxes {
				if sb.Owner == owner {
					over = append(over, sb)
				}
			}
		}
	}
	s.mu.Unlock()
	for _, sb := range over {
		s.log().Warn("daily minutes used up; stopping sandbox", "sandbox", sb.ID, "owner", sb.Owner)
		s.hostCall(ctx, http.MethodDelete, sb.HostURL+"/v1/sandboxes/"+sb.ID, nil)
		s.mu.Lock()
		if cur, ok := s.sandboxes[sb.ID]; ok {
			s.accrue(cur, s.now())
			delete(s.sandboxes, sb.ID)
		}
		s.mu.Unlock()
		s.save()
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !tokenOK(r, s.HostSecret) {
		host.WriteError(w, http.StatusUnauthorized, "invalid host secret")
		return
	}
	var req api.RegisterHostRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		host.WriteError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	u, err := url.Parse(req.URL)
	if req.Name == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		host.WriteError(w, http.StatusBadRequest, "name and an http(s) url are required")
		return
	}
	reported := make(map[string]bool, len(req.Sandboxes))
	for _, id := range req.Sandboxes {
		reported[id] = true
	}
	var resp api.RegisterHostResponse
	changed := false
	s.mu.Lock()
	if s.hosts == nil {
		s.hosts = map[string]*hostState{}
	}
	s.hosts[req.Name] = &hostState{RegisterHostRequest: req, lastSeen: s.now()}
	for _, id := range req.Sandboxes {
		if sb, ok := s.sandboxes[id]; !ok || sb.Host != req.Name {
			resp.Destroy = append(resp.Destroy, id)
		}
	}
	for id, sb := range s.sandboxes {
		if sb.Host == req.Name && !reported[id] && s.now().Sub(sb.CreatedAt) > createGrace {
			s.accrue(sb, s.now())
			delete(s.sandboxes, id)
			changed = true
			s.log().Info("sandbox gone from its host; record dropped", "sandbox", id, "host", req.Name)
		}
	}
	s.mu.Unlock()
	if changed {
		s.save()
	}
	for _, id := range resp.Destroy {
		s.log().Warn("host runs a sandbox with no record; destroying it", "sandbox", id, "host", req.Name)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) hostLive(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hosts[name]
	return ok && s.now().Sub(h.lastSeen) <= HostTimeout
}

// pick chooses the live host with the fewest sandboxes and free capacity.
func (s *Server) pick() (*hostState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	load := map[string]int{}
	for _, sb := range s.sandboxes {
		load[sb.Host]++
	}
	var live []*hostState
	for _, h := range s.hosts {
		if s.now().Sub(h.lastSeen) > HostTimeout {
			continue
		}
		if h.Capacity > 0 && load[h.Name] >= h.Capacity {
			continue
		}
		live = append(live, h)
	}
	if len(live) == 0 {
		return nil, fmt.Errorf("no host has room for a sandbox")
	}
	sort.Slice(live, func(i, j int) bool {
		if load[live[i].Name] != load[live[j].Name] {
			return load[live[i].Name] < load[live[j].Name]
		}
		return live[i].Name < live[j].Name
	})
	return live[0], nil
}

func (s *Server) hostCall(ctx context.Context, method, url string, body any) error {
	_, err := s.hostCallBody(ctx, method, url, body)
	return err
}

// hostCallBody is hostCall, returning the response body.
func (s *Server) hostCallBody(ctx context.Context, method, url string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.HostSecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e api.Error
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		return nil, fmt.Errorf("%s: %s", resp.Status, e.Error)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var req api.CreateSandboxRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		host.WriteError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	if err := req.Spec.Validate(); err != nil {
		host.WriteError(w, http.StatusBadRequest, "%v", err)
		return
	}
	p := principal(r)
	if msg, ok := s.admit(p, &req.Spec); !ok {
		host.WriteError(w, http.StatusTooManyRequests, "%s", msg)
		return
	}
	h, err := s.pick()
	if err != nil {
		host.WriteError(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	sb := &api.Sandbox{
		ID:        api.NewID("sbx"),
		Host:      h.Name,
		HostURL:   strings.TrimRight(h.URL, "/"),
		Token:     api.NewToken(),
		Spec:      req.Spec,
		CreatedAt: s.now().UTC(),
		Owner:     p.Name,
	}
	// Count it against the host before the slow call, so concurrent
	// creates spread out.
	rec := *sb
	rec.Token = ""
	s.mu.Lock()
	if s.sandboxes == nil {
		s.sandboxes = map[string]*api.Sandbox{}
	}
	s.sandboxes[sb.ID] = &rec
	s.mu.Unlock()

	err = s.hostCall(r.Context(), http.MethodPost, sb.HostURL+"/v1/sandboxes",
		api.HostCreateRequest{ID: sb.ID, Token: sb.Token, Spec: sb.Spec})
	if err != nil {
		s.mu.Lock()
		delete(s.sandboxes, sb.ID)
		s.mu.Unlock()
		s.log().Error("host create failed", "host", h.Name, "sandbox", sb.ID, "err", err)
		host.WriteError(w, http.StatusBadGateway, "host %s: %v", h.Name, err)
		return
	}
	s.save()
	s.log().Info("sandbox placed", "sandbox", sb.ID, "host", h.Name)
	writeJSON(w, http.StatusCreated, sb)
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := make([]api.Sandbox, 0, len(s.sandboxes))
	for _, sb := range s.sandboxes {
		c := *sb
		c.Token = "" // tokens are only ever returned once
		out = append(out, c)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) destroy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	sb, ok := s.sandboxes[id]
	s.mu.Unlock()
	if !ok {
		host.WriteError(w, http.StatusNotFound, "sandbox %s not found", id)
		return
	}
	// The host answers with the sandbox's signed record. It is passed on
	// to the caller and not kept here.
	rec, err := s.hostCallBody(r.Context(), http.MethodDelete, sb.HostURL+"/v1/sandboxes/"+id, nil)
	if err != nil {
		if s.hostLive(sb.Host) {
			host.WriteError(w, http.StatusBadGateway, "host %s: %v", sb.Host, err)
			return
		}
		// The host is offline. Drop the record; when the host comes back,
		// its heartbeat lists the sandbox and it is told to destroy it.
		s.log().Warn("host offline; dropping sandbox record", "sandbox", id, "host", sb.Host)
	}
	s.mu.Lock()
	s.accrue(sb, s.now())
	delete(s.sandboxes, id)
	s.mu.Unlock()
	s.save()
	s.log().Info("sandbox destroyed", "sandbox", id, "host", sb.Host)
	if len(rec) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(rec)
}

// hosts lists the hosts with their public keys, so records can be checked.
func (s *Server) hostList(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := make([]api.Host, 0, len(s.hosts))
	now := s.now()
	for _, h := range s.hosts {
		out = append(out, api.Host{
			Name:      h.Name,
			PublicKey: h.PublicKey,
			LastSeen:  h.lastSeen.UTC(),
			URL:       h.URL,
			Online:    now.Sub(h.lastSeen) <= HostTimeout,
		})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}
