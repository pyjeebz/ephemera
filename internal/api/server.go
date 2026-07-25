// Package api serves ephemerad's control plane.
//
// The API is deliberately small: create a machine, list them, run something
// inside one, destroy it. Everything is JSON except exec, which streams
// newline-delimited frames so output arrives while the command is still
// running rather than in one lump at the end.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/cgroup"
	"github.com/pyjeebz/ephemera/internal/machine"
	"github.com/pyjeebz/ephemera/internal/store"
	"github.com/pyjeebz/ephemera/internal/vmnet"
	"github.com/pyjeebz/ephemera/internal/webui"
)

// Config holds what the daemon needs to build machines.
type Config struct {
	KernelPath string
	RootfsPath string
	RunDir     string
	VCPUs      int
	MemMiB     int

	// DesktopRootfs is the heavier image for desktop boxes (headless X + VNC).
	// Empty means the daemon has no desktop image, and desktop requests are
	// refused rather than served the terminal-only rootfs, which has no X stack.
	DesktopRootfs string

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
	cfg       Config
	store     *store.Store
	snaps     *store.SnapshotStore
	computers *store.ComputerStore
	log       *slog.Logger
}

// New returns a server backed by the machine, snapshot, and computer stores.
func New(cfg Config, st *store.Store, snaps *store.SnapshotStore, computers *store.ComputerStore, log *slog.Logger) *Server {
	if cfg.VCPUs == 0 {
		cfg.VCPUs = machine.DefaultVCPUs
	}
	if cfg.MemMiB == 0 {
		cfg.MemMiB = machine.DefaultMemMiB
	}
	if cfg.BootTimeout == 0 {
		cfg.BootTimeout = 30 * time.Second
	}
	return &Server{cfg: cfg, store: st, snaps: snaps, computers: computers, log: log}
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
	mux.HandleFunc("GET /v1/machines/{id}/desktop/ws", s.desktopWS)
	mux.HandleFunc("GET /v1/machines/{id}/shell/ws", s.shellWS)
	mux.HandleFunc("POST /v1/machines/{id}/snapshot", s.snapshot)
	mux.HandleFunc("GET /v1/snapshots", s.listSnapshots)
	mux.HandleFunc("DELETE /v1/snapshots/{id}", s.deleteSnapshot)
	mux.HandleFunc("POST /v1/snapshots/{id}/fork", s.fork)
	mux.HandleFunc("POST /v1/computers", s.createComputer)
	mux.HandleFunc("GET /v1/computers", s.listComputers)
	mux.HandleFunc("GET /v1/computers/{name}", s.getComputer)
	mux.HandleFunc("POST /v1/computers/{name}/start", s.startComputer)
	mux.HandleFunc("POST /v1/computers/{name}/stop", s.stopComputer)
	mux.HandleFunc("DELETE /v1/computers/{name}", s.deleteComputer)

	// The web UI is the catch-all: every path the API routes above did not claim
	// falls to the SPA, which serves its index and routes on the client. It is
	// only reachable to a browser on the opt-in -http surface. Nil when the UI was
	// not built into this binary, in which case unknown paths just 404.
	if ui := webui.Handler(); ui != nil {
		mux.Handle("GET /", ui)
	}
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

	// Desktop boots a graphical box: the heavier desktop image, a desktop init,
	// and bigger default resources. Refused when the daemon has no desktop image.
	Desktop bool `json:"desktop,omitempty"`
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

	// VsockPath is the host socket that reaches the guest agent. A local client
	// (same host as the daemon) connects to it directly for an interactive shell,
	// which needs a full-duplex byte stream the HTTP API does not carry.
	VsockPath string `json:"vsock_path,omitempty"`

	// Computer is the persistent computer this machine backs, empty for an
	// anonymous ephemeral machine — so a listing can show computers once and
	// ephemeral machines separately.
	Computer string `json:"computer,omitempty"`
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
		VsockPath: r.VsockPath,
		Computer:  r.Computer,
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

	// A desktop box swaps in the heavier image, a desktop init, and bigger default
	// resources. An explicit -cpus/-mem still wins; the desktop defaults only fill
	// in what the request left as zero.
	rootfs, init := s.cfg.RootfsPath, machine.AgentInit
	vcpus, mem := cmp(req.VCPUs, s.cfg.VCPUs), cmp(req.MemMiB, s.cfg.MemMiB)
	if req.Desktop {
		if s.cfg.DesktopRootfs == "" {
			writeError(w, http.StatusBadRequest, errors.New(
				"this daemon has no desktop image: build it with 'build/build-rootfs.sh --desktop' and start ephemerad with -desktop-rootfs"))
			return
		}
		if _, err := os.Stat(s.cfg.DesktopRootfs); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("desktop image not usable: %w", err))
			return
		}
		rootfs, init = s.cfg.DesktopRootfs, machine.DesktopInit
		vcpus = cmp(req.VCPUs, machine.DefaultDesktopVCPUs)
		mem = cmp(req.MemMiB, machine.DefaultDesktopMemMiB)
	}

	cfg := machine.Config{
		KernelPath: s.cfg.KernelPath,
		RootfsPath: rootfs,
		RunDir:     s.cfg.RunDir,
		VCPUs:      vcpus,
		MemMiB:     mem,
		Init:       init,
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

	rec, err := s.bootAndTrack(ctx, cfg, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.log.Info("machine created", "id", rec.ID, "pid", rec.PID, "vcpus", rec.VCPUs,
		"mem_mib", rec.MemMiB, "guest_ip", rec.GuestIP)
	writeJSON(w, http.StatusCreated, toResponse(rec))
}

// bootAndTrack boots a machine, waits for its agent, records it (linking it to a
// computer when computer is non-empty), and returns the record. On any failure
// it leaves nothing running.
func (s *Server) bootAndTrack(ctx context.Context, cfg machine.Config, computer string) (store.Record, error) {
	m, err := machine.Boot(ctx, cfg)
	if err != nil {
		return store.Record{}, err
	}
	if err := m.WaitAgent(ctx); err != nil {
		_ = m.Destroy(context.Background())
		return store.Record{}, err
	}

	rec := store.Record{
		ID:        m.ID,
		PID:       m.Pid(),
		APISock:   s.cfg.RunDir + "/" + m.ID + ".sock",
		VsockPath: m.VsockPath(),
		VCPUs:     cfg.VCPUs,
		MemMiB:    cfg.MemMiB,
		StartedAt: m.StartedAt,
		Computer:  computer,
	}
	if lease, ok := m.Lease(); ok {
		rec.Tap, rec.GuestIP = lease.Tap, lease.Guest.String()
	}
	rec.Cgroup = m.CgroupPath()
	if dir, ok := m.Jailed(); ok {
		rec.JailDir = dir
	}
	if err := s.store.Add(m, rec); err != nil {
		_ = m.Destroy(context.Background())
		return store.Record{}, err
	}
	return rec, nil
}

// machineConfig returns the daemon's base machine config, before per-request
// tweaks like network, resources, or a persist disk.
func (s *Server) machineConfig() machine.Config {
	return machine.Config{
		KernelPath: s.cfg.KernelPath,
		RootfsPath: s.cfg.RootfsPath,
		RunDir:     s.cfg.RunDir,
		VCPUs:      s.cfg.VCPUs,
		MemMiB:     s.cfg.MemMiB,
		Init:       machine.AgentInit,
		DNS:        s.cfg.DNS,
		Cgroup:     s.cfg.Cgroup,
		Jail:       s.cfg.Jail,
		JailHelper: s.cfg.JailHelper,
	}
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

// SnapshotResponse describes a snapshot to a client.
type SnapshotResponse struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"source_id"`
	VCPUs     int       `json:"vcpus"`
	MemMiB    int       `json:"mem_mib"`
	CreatedAt time.Time `json:"created_at"`
}

func toSnapshotResponse(r store.SnapshotRecord) SnapshotResponse {
	return SnapshotResponse{
		ID:        r.ID,
		SourceID:  r.SourceID,
		VCPUs:     r.VCPUs,
		MemMiB:    r.MemMiB,
		CreatedAt: r.CreatedAt,
	}
}

// snapshot freezes a machine to disk and records the result, leaving the machine
// running. Only a jailed machine can be snapshotted, because only a jailed
// snapshot can be forked — its guest agent socket is chroot-relative and so
// distinct per copy.
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, _, err := s.store.Get(id)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if _, ok := m.Jailed(); !ok {
		writeError(w, http.StatusBadRequest,
			errors.New("only a jailed machine can be snapshotted; start ephemerad with -jail"))
		return
	}

	snapID, err := newID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	snap, err := m.Snapshot(ctx, filepath.Join(s.snaps.Dir(), snapID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Snapshot pauses the guest; put it back to work — the snapshot is a copy,
	// not a handover.
	if err := m.Resume(ctx); err != nil {
		s.log.Error("resume after snapshot failed", "id", id, "err", err)
	}

	rec, err := s.snaps.Add(snapID, snap)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("snapshot created", "id", snapID, "source", id)
	writeJSON(w, http.StatusCreated, toSnapshotResponse(rec))
}

func (s *Server) listSnapshots(w http.ResponseWriter, _ *http.Request) {
	recs := s.snaps.List()
	out := make([]SnapshotResponse, 0, len(recs))
	for _, r := range recs {
		out = append(out, toSnapshotResponse(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func (s *Server) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := s.snaps.Remove(r.PathValue("id")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fork restores a snapshot into a new running machine. Because the machines come
// from one read-only base and each keeps its writes in its own RAM overlay, any
// number of forks of one snapshot run side by side.
func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	rec, err := s.snaps.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.BootTimeout)
	defer cancel()

	m, err := machine.Restore(ctx, machine.RestoreConfig{
		Snapshot:   rec.Snapshot(),
		RunDir:     s.cfg.RunDir,
		JailHelper: s.cfg.JailHelper,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	mrec := store.Record{
		ID:        m.ID,
		PID:       m.Pid(),
		VsockPath: m.VsockPath(),
		VCPUs:     rec.VCPUs,
		MemMiB:    rec.MemMiB,
		StartedAt: m.StartedAt,
	}
	if dir, ok := m.Jailed(); ok {
		mrec.JailDir = dir
	}
	if err := s.store.Add(m, mrec); err != nil {
		_ = m.Destroy(context.Background())
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("machine forked", "id", m.ID, "snapshot", rec.ID, "pid", m.Pid())
	writeJSON(w, http.StatusCreated, toResponse(mrec))
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

// newID returns a short, collision-resistant id for a snapshot.
func newID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
