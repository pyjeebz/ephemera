// Package control is the central server. It knows which hosts exist and
// which sandboxes run where. It handles ids and states only: files, command
// output and secrets go straight between the CLI and the host.
//
// Running sandboxes and usage are kept in a StateStore, so a restart keeps
// track of them. Every host heartbeat lists what the host really runs:
// records the host no longer has are dropped, and sandboxes the central
// server does not know about are destroyed.
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/host"
)

// HostTimeout is how long a host may go without a heartbeat before its
// connection is taken for dead and closed.
const HostTimeout = 45 * time.Second

// Server serves the central API.
type Server struct {
	Tokens    Tokens     // API tokens for CLI requests
	State     StateStore // where sandboxes and usage are kept
	Hosts     HostStore  // hosts that joined, and join codes; nil is a FileHosts next to StatePath, or in memory
	StatePath string     // with no State: a FileState at this path; empty keeps it in memory
	// GitHub and Accounts turn on web sign-in and the dashboard's token
	// endpoints. Without both, those routes don't exist.
	GitHub   *GitHub
	Accounts AccountStore
	Log      *slog.Logger
	Now      func() time.Time

	stateOnce sync.Once
	state     StateStore
	hostsOnce sync.Once
	hostStore HostStore

	// The running sandboxes and recent usage, also in the StateStore, are
	// kept here so requests don't wait on it. One server process per store.
	mu        sync.Mutex
	hosts     map[string]*hostState // by host id
	sandboxes map[string]*api.Sandbox
	// usage is finished sandboxes' seconds, by owner and UTC day.
	usage map[owner]map[string]float64
	// limits is each owner's limits as last seen on a request.
	limits map[owner]Limits
}

// owner is a token, in its account. Usage and limits are counted per owner.
type owner struct{ Account, Name string }

func ownerOf(sb *api.Sandbox) owner { return owner{accountOr(sb.Account), sb.Owner} }

func (s *Server) hostRecords() HostStore {
	s.hostsOnce.Do(func() {
		switch {
		case s.Hosts != nil:
			s.hostStore = s.Hosts
		case s.StatePath != "":
			s.hostStore = &FileHosts{Path: filepath.Join(filepath.Dir(s.StatePath), "hosts.json")}
		default:
			s.hostStore = &FileHosts{}
		}
	})
	return s.hostStore
}

func (s *Server) store() StateStore {
	s.stateOnce.Do(func() {
		switch {
		case s.State != nil:
			s.state = s.State
		case s.StatePath != "":
			s.state = &FileState{Path: s.StatePath, Now: s.Now}
		default:
			s.state = memState{}
		}
	})
	return s.state
}

// createGrace is how long a new sandbox may be missing from its host's
// heartbeat before its record is dropped: the host may still be creating it.
const createGrace = 2 * time.Minute

// Load reads the running sandboxes and the last month of usage from the
// StateStore.
func (s *Server) Load() error {
	ctx := context.Background()
	running, err := s.store().RunningSandboxes(ctx)
	if err != nil {
		return err
	}
	// Sandboxes from before hosts joined with keys have no host id, and no
	// host can claim them.
	keep := running[:0]
	for _, sb := range running {
		if sb.HostID == "" {
			s.store().EndSandbox(ctx, sb.ID, s.now().UTC(), EndGone)
			continue
		}
		keep = append(keep, sb)
	}
	running = keep
	since := s.now().UTC().Add(-usageKeep).Format(dayFormat)
	used, err := s.store().UsageSince(ctx, since)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sandboxes = make(map[string]*api.Sandbox, len(running))
	for i := range running {
		sb := running[i]
		sb.Token = ""
		sb.Account = accountOr(sb.Account)
		s.sandboxes[sb.ID] = &sb
	}
	s.usage = map[owner]map[string]float64{}
	for _, u := range used {
		o := owner{u.Account, u.Owner}
		if s.usage[o] == nil {
			s.usage[o] = map[string]float64{}
		}
		s.usage[o][u.Day] += u.Seconds
	}
	return nil
}

// end forgets a sandbox and records why it ended and the time it used.
func (s *Server) end(ctx context.Context, sb *api.Sandbox, reason string) {
	at := s.now()
	s.mu.Lock()
	cur, ok := s.sandboxes[sb.ID]
	var used []Usage
	if ok {
		used = s.accrue(cur, at)
		delete(s.sandboxes, sb.ID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	// The record is already gone from memory; a failed write is logged,
	// as there is nothing better to do with it.
	if err := s.store().EndSandbox(ctx, sb.ID, at.UTC(), reason); err != nil {
		s.log().Error("store: end sandbox", "sandbox", sb.ID, "err", err)
	}
	for _, u := range used {
		if err := s.store().AddUsage(ctx, u); err != nil {
			s.log().Error("store: add usage", "sandbox", sb.ID, "err", err)
		}
	}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /v1/hosts/connect", s.hostConnect)
	mux.HandleFunc("POST /v1/hosts/join", s.hostJoin)
	mux.HandleFunc("DELETE /v1/hosts/{id}", s.user(s.hostRemove))
	mux.HandleFunc("GET /v1/whoami", s.user(s.whoami))
	mux.HandleFunc("GET /v1/usage", s.user(s.usageHandler))
	mux.HandleFunc("GET /v1/hosts", s.user(s.hostList))
	mux.HandleFunc("POST /v1/sandboxes", s.user(s.create))
	mux.HandleFunc("GET /v1/sandboxes", s.user(s.list))
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", s.user(s.destroy))
	if !s.webEnabled() {
		return mux
	}
	s.webRoutes(mux)
	return s.cors(mux)
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

type ctxKey struct{}

func (s *Server) user(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var p Principal
		ok := false
		if s.Tokens != nil && tok != "" {
			p, ok = s.Tokens.Check(r.Context(), tok)
		}
		if !ok {
			host.WriteError(w, http.StatusUnauthorized, "invalid token; run ephemera login")
			return
		}
		p.Account = accountOr(p.Account)
		s.mu.Lock()
		if s.limits == nil {
			s.limits = map[owner]Limits{}
		}
		s.limits[owner{p.Account, p.Name}] = p.Limits
		s.mu.Unlock()
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	}
}

func principal(r *http.Request) Principal {
	p, _ := r.Context().Value(ctxKey{}).(Principal)
	return p
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	writeJSON(w, http.StatusOK, api.WhoAmI{Name: p.Name, Account: p.Account})
}

const dayFormat = "2006-01-02"

// accrue adds a finished sandbox's time to its owner's usage, split by UTC
// day, and returns what it added. Locked.
func (s *Server) accrue(sb *api.Sandbox, end time.Time) []Usage {
	if sb.Owner == "" {
		return nil
	}
	o := ownerOf(sb)
	if s.usage == nil {
		s.usage = map[owner]map[string]float64{}
	}
	if s.usage[o] == nil {
		s.usage[o] = map[string]float64{}
	}
	var out []Usage
	for start := sb.CreatedAt.UTC(); start.Before(end); {
		dayEnd := time.Date(start.Year(), start.Month(), start.Day()+1, 0, 0, 0, 0, time.UTC)
		stop := end
		if dayEnd.Before(stop) {
			stop = dayEnd
		}
		day, secs := start.Format(dayFormat), stop.Sub(start).Seconds()
		s.usage[o][day] += secs
		out = append(out, Usage{Account: o.Account, Owner: o.Name, Day: day, Seconds: secs})
		start = stop
	}
	return out
}

// usedToday is an owner's sandbox-seconds today, running ones included,
// and how many are running. Locked.
func (s *Server) usedToday(o owner) (float64, int) {
	now := s.now().UTC()
	today := now.Format(dayFormat)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	used := s.usage[o][today]
	running := 0
	for _, sb := range s.sandboxes {
		if ownerOf(sb) != o {
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
	used, running := s.usedToday(owner{p.Account, p.Name})
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
	used, running := s.usedToday(owner{p.Account, p.Name})
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
	for o, l := range s.limits {
		if l.MaxMinutesPerDay <= 0 {
			continue
		}
		if used, _ := s.usedToday(o); used >= float64(l.MaxMinutesPerDay*60) {
			for _, sb := range s.sandboxes {
				if ownerOf(sb) == o {
					over = append(over, sb)
				}
			}
		}
	}
	s.mu.Unlock()
	for _, sb := range over {
		s.log().Warn("daily minutes used up; stopping sandbox", "sandbox", sb.ID, "owner", sb.Owner)
		if _, err := s.hostDo(ctx, sb.HostID, api.HostMessage{Type: "destroy", Sandbox: sb.ID}); err != nil {
			s.log().Warn("stopping sandbox", "sandbox", sb.ID, "host", sb.Host, "err", err)
		}
		s.end(ctx, sb, EndLimit)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) hostLive(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hosts[id]
	return ok && h.conn != nil
}

// pick chooses, among the account's connected hosts, the one with the
// fewest sandboxes and free capacity. Sandboxes only ever go to the
// account's own hosts.
func (s *Server) pick(account string) (*hostState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	load := map[string]int{}
	for _, sb := range s.sandboxes {
		load[sb.HostID]++
	}
	var live []*hostState
	for _, h := range s.hosts {
		if h.conn == nil || h.Account != account {
			continue
		}
		if h.Capacity > 0 && load[h.ID] >= h.Capacity {
			continue
		}
		live = append(live, h)
	}
	if len(live) == 0 {
		return nil, fmt.Errorf("none of this account's hosts is connected with room for a sandbox; ephemera doctor says why")
	}
	sort.Slice(live, func(i, j int) bool {
		if load[live[i].ID] != load[live[j].ID] {
			return load[live[i].ID] < load[live[j].ID]
		}
		return live[i].Name < live[j].Name
	})
	return live[0], nil
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
	h, err := s.pick(p.Account)
	if err != nil {
		host.WriteError(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	sb := &api.Sandbox{
		ID:        api.NewID("sbx"),
		Host:      h.Name,
		HostID:    h.ID,
		HostURL:   strings.TrimRight(h.URL, "/"),
		Token:     api.NewToken(),
		Spec:      req.Spec,
		CreatedAt: s.now().UTC(),
		Owner:     p.Name,
		Account:   p.Account,
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
	// Stored before the host makes it, so a crash in between leaves a
	// record the host's heartbeat clears up, not a sandbox nobody knows.
	if err := s.store().StartSandbox(r.Context(), rec); err != nil {
		s.mu.Lock()
		delete(s.sandboxes, sb.ID)
		s.mu.Unlock()
		s.log().Error("store: start sandbox", "sandbox", sb.ID, "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "the central server could not save the sandbox; try again")
		return
	}

	_, err = s.hostDo(r.Context(), h.ID, api.HostMessage{Type: "create",
		Create: &api.HostCreateRequest{ID: sb.ID, Token: sb.Token, Spec: sb.Spec}})
	if err != nil {
		s.mu.Lock()
		delete(s.sandboxes, sb.ID)
		s.mu.Unlock()
		if err := s.store().EndSandbox(r.Context(), sb.ID, s.now().UTC(), EndFailed); err != nil {
			s.log().Error("store: end sandbox", "sandbox", sb.ID, "err", err)
		}
		s.log().Error("host create failed", "host", h.Name, "sandbox", sb.ID, "err", err)
		host.WriteError(w, http.StatusBadGateway, "host %s: %v", h.Name, err)
		return
	}
	s.log().Info("sandbox placed", "sandbox", sb.ID, "host", h.Name)
	writeJSON(w, http.StatusCreated, sb)
}

// list returns the caller's account's sandboxes.
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	account := principal(r).Account
	s.mu.Lock()
	out := []api.Sandbox{}
	for _, sb := range s.sandboxes {
		if sb.Account != account {
			continue
		}
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
	// Another account's sandbox is "not found" too, so ids can't be probed.
	if !ok || sb.Account != principal(r).Account {
		host.WriteError(w, http.StatusNotFound, "sandbox %s not found", id)
		return
	}
	// The host answers with the sandbox's signed record. It is passed on
	// to the caller and not kept here.
	res, err := s.hostDo(r.Context(), sb.HostID, api.HostMessage{Type: "destroy", Sandbox: id})
	if err != nil {
		if s.hostLive(sb.HostID) {
			host.WriteError(w, http.StatusBadGateway, "host %s: %v", sb.Host, err)
			return
		}
		// The host is offline. Drop the record; when the host comes back,
		// its heartbeat lists the sandbox and it is told to destroy it.
		s.log().Warn("host offline; dropping sandbox record", "sandbox", id, "host", sb.Host)
	}
	s.end(r.Context(), sb, EndDestroyed)
	s.log().Info("sandbox destroyed", "sandbox", id, "host", sb.Host)
	if res.Record == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, res.Record)
}
