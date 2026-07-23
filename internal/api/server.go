// Package api serves ephemerad's control plane.
//
// The API is deliberately small: create a machine, list them, run something
// inside one, destroy it. Everything is JSON except exec, which streams
// newline-delimited frames so output arrives while the command is still
// running rather than in one lump at the end.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/cgroup"
	"github.com/pyjeebz/ephemera/internal/machine"
	"github.com/pyjeebz/ephemera/internal/store"
	"github.com/pyjeebz/ephemera/internal/vmnet"
)

// Config holds what the daemon needs to build machines.
type Config struct {
	KernelPath string
	RootfsPath string
	RunDir     string
	VCPUs      int
	MemMiB     int

	// BootTimeout bounds how long a create waits for the guest agent.
	BootTimeout time.Duration

	// Net is the machine network. Nil means the daemon cannot give machines an
	// interface, and requests that ask for one are refused rather than quietly
	// served a machine that is not what was asked for.
	Net *vmnet.Manager

	// DNS is the resolver networked guests are pointed at.
	DNS netip.Addr

	// Cgroup caps every machine's CPU and memory when set. Nil means the daemon
	// could not find its delegated subtree and machines run uncapped.
	Cgroup *cgroup.Manager

	// Jail confines every machine's VMM when set; JailHelper is the eph-jail
	// binary. Like caps, it is a restriction applied to every machine rather than
	// something a request opts into.
	Jail       bool
	JailHelper string
}

// Server implements the control plane.
type Server struct {
	cfg   Config
	store *store.Store
	log   *slog.Logger
}

// New returns a server backed by store.
func New(cfg Config, st *store.Store, log *slog.Logger) *Server {
	if cfg.VCPUs == 0 {
		cfg.VCPUs = machine.DefaultVCPUs
	}
	if cfg.MemMiB == 0 {
		cfg.MemMiB = machine.DefaultMemMiB
	}
	if cfg.BootTimeout == 0 {
		cfg.BootTimeout = 30 * time.Second
	}
	return &Server{cfg: cfg, store: st, log: log}
}

// Handler returns the routed control plane.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/machines", s.create)
	mux.HandleFunc("GET /v1/machines", s.list)
	mux.HandleFunc("GET /v1/machines/{id}", s.get)
	mux.HandleFunc("DELETE /v1/machines/{id}", s.destroy)
	mux.HandleFunc("POST /v1/machines/{id}/exec", s.exec)
	return s.logRequests(mux)
}

// CreateRequest asks for a new machine. Zero fields take the daemon's defaults.
type CreateRequest struct {
	VCPUs  int `json:"vcpus,omitempty"`
	MemMiB int `json:"mem_mib,omitempty"`

	// Network asks for an interface. It is off by default, and stays that way:
	// a machine that cannot reach anything is the isolation floor, and every
	// step above it should be something a caller asked for out loud.
	Network bool `json:"network,omitempty"`
}

// MachineResponse describes a machine to a client.
type MachineResponse struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	VCPUs     int       `json:"vcpus"`
	MemMiB    int       `json:"mem_mib"`
	StartedAt time.Time `json:"started_at"`
	UptimeSec float64   `json:"uptime_sec"`
	GuestIP   string    `json:"guest_ip,omitempty"`
	Tap       string    `json:"tap,omitempty"`
}

func toResponse(r store.Record) MachineResponse {
	return MachineResponse{
		ID:        r.ID,
		PID:       r.PID,
		VCPUs:     r.VCPUs,
		MemMiB:    r.MemMiB,
		StartedAt: r.StartedAt,
		UptimeSec: time.Since(r.StartedAt).Seconds(),
		GuestIP:   r.GuestIP,
		Tap:       r.Tap,
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// create boots a machine and waits for its agent before reporting success, so a
// machine that exists to a client is always one that can actually run commands.
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
			return
		}
	}

	if req.Network && s.cfg.Net == nil {
		writeError(w, http.StatusBadRequest,
			errors.New("this daemon cannot give machines a network: start ephemerad with -network"))
		return
	}

	cfg := machine.Config{
		KernelPath: s.cfg.KernelPath,
		RootfsPath: s.cfg.RootfsPath,
		RunDir:     s.cfg.RunDir,
		VCPUs:      cmp(req.VCPUs, s.cfg.VCPUs),
		MemMiB:     cmp(req.MemMiB, s.cfg.MemMiB),
		Init:       machine.AgentInit,
		DNS:        s.cfg.DNS,
		Cgroup:     s.cfg.Cgroup,
		Jail:       s.cfg.Jail,
		JailHelper: s.cfg.JailHelper,
	}
	if req.Network {
		cfg.Net = s.cfg.Net
	}

	// Detached from the request: a client that hangs up mid-create should not
	// leave a half-booted machine behind.
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.BootTimeout)
	defer cancel()

	m, err := machine.Boot(ctx, cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := m.WaitAgent(ctx); err != nil {
		_ = m.Destroy(context.Background())
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	rec := store.Record{
		ID:        m.ID,
		PID:       m.Pid(),
		APISock:   s.cfg.RunDir + "/" + m.ID + ".sock",
		VsockPath: m.VsockPath(),
		VCPUs:     cfg.VCPUs,
		MemMiB:    cfg.MemMiB,
		StartedAt: m.StartedAt,
	}
	if lease, ok := m.Lease(); ok {
		rec.Tap, rec.GuestIP = lease.Tap, lease.Guest.String()
	}
	rec.Cgroup = m.CgroupPath()
	if err := s.store.Add(m, rec); err != nil {
		_ = m.Destroy(context.Background())
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.log.Info("machine created", "id", m.ID, "pid", m.Pid(), "vcpus", cfg.VCPUs,
		"mem_mib", cfg.MemMiB, "guest_ip", rec.GuestIP)
	writeJSON(w, http.StatusCreated, toResponse(rec))
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request) {
	recs := s.store.List()
	out := make([]MachineResponse, 0, len(recs))
	for _, r := range recs {
		out = append(out, toResponse(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": out})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	_, rec, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(rec))
}

func (s *Server) destroy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, _, err := s.store.Get(id)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Destroy(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.Remove(id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.log.Info("machine destroyed", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

// exec runs a command in a machine, streaming the agent's frames straight
// through to the client as newline-delimited JSON.
//
// Each frame is flushed as it arrives; without that, Go's buffering would hold
// output back and a long-running command would look hung.
func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	m, _, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}

	var req agent.ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if len(req.Cmd) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("cmd must not be empty"))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}

	// The status line has to be committed before the first frame, so failures
	// after this point are reported inside the stream rather than as a code.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	enc := json.NewEncoder(w)
	sink := func(kind agent.FrameKind) writerFunc {
		return func(p []byte) (int, error) {
			if err := enc.Encode(agent.Frame{Kind: kind, Data: p}); err != nil {
				return 0, err
			}
			flusher.Flush()
			return len(p), nil
		}
	}

	code, err := m.Exec(r.Context(), req.Cmd, sink(agent.FrameStdout), sink(agent.FrameStderr))
	if err != nil {
		_ = enc.Encode(agent.Frame{Kind: agent.FrameError, Error: err.Error()})
		flusher.Flush()
		return
	}
	_ = enc.Encode(agent.Frame{Kind: agent.FrameExit, Code: code})
	flusher.Flush()
}

// DestroyAll tears down every machine the daemon owns. Used on shutdown so a
// stopping daemon does not leave VMMs behind for the next one to reap.
func (s *Server) DestroyAll(ctx context.Context) {
	for _, rec := range s.store.List() {
		m, _, err := s.store.Get(rec.ID)
		if err != nil {
			continue
		}
		if err := m.Destroy(ctx); err != nil {
			s.log.Error("destroy on shutdown failed", "id", rec.ID, "err", err)
		}
		_ = s.store.Remove(rec.ID)
		s.log.Info("machine destroyed on shutdown", "id", rec.ID)
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
	})
}

// writerFunc adapts a function to io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func statusFor(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// cmp returns v when set, otherwise fallback.
func cmp(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
