// Package control is the central server. It knows which hosts exist and
// which sandboxes run where. It handles ids and states only: files, command
// output and secrets go straight between the CLI and the host.
//
// State is kept in memory for now; a restart forgets running sandboxes, and
// their hosts destroy them when they in turn restart.
package control

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	UserToken  string // bearer token for CLI requests
	HostSecret string // shared with every host
	Client     *http.Client
	Log        *slog.Logger
	Now        func() time.Time

	mu        sync.Mutex
	hosts     map[string]*hostState
	sandboxes map[string]*api.Sandbox
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

func (s *Server) user(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !tokenOK(r, s.UserToken) {
			host.WriteError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		h(w, r)
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
	s.mu.Lock()
	if s.hosts == nil {
		s.hosts = map[string]*hostState{}
	}
	s.hosts[req.Name] = &hostState{RegisterHostRequest: req, lastSeen: s.now()}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
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
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.HostSecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e api.Error
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		return fmt.Errorf("%s: %s", resp.Status, e.Error)
	}
	return nil
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
	}
	// Count it against the host before the slow call, so concurrent
	// creates spread out.
	s.mu.Lock()
	if s.sandboxes == nil {
		s.sandboxes = map[string]*api.Sandbox{}
	}
	s.sandboxes[sb.ID] = sb
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
	if err := s.hostCall(r.Context(), http.MethodDelete, sb.HostURL+"/v1/sandboxes/"+id, nil); err != nil {
		host.WriteError(w, http.StatusBadGateway, "host %s: %v", sb.Host, err)
		return
	}
	s.mu.Lock()
	delete(s.sandboxes, id)
	s.mu.Unlock()
	s.log().Info("sandbox destroyed", "sandbox", id, "host", sb.Host)
	w.WriteHeader(http.StatusNoContent)
}
