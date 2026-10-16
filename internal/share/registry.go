// Package share holds the relay's share registry and TTL sweeper:
// which shares exist, who is serving them, and when they expire.
package share

import (
	"errors"
	"sync"
	"time"
)

// ErrNotFound, ErrGone: the two ways a lookup fails. Callers map
// them to 404 and 410 respectively.
var (
	ErrNotFound = errors.New("share: not found")
	ErrGone     = errors.New("share: expired")
)

// ErrKilled is returned by Register for a share an admin killed: the
// sender's daemon is still redialing, and the kill must stick.
var ErrKilled = errors.New("share: killed")

// Info is one registered share: the announcement a daemon made.
type Info struct {
	ID      string
	Name    string
	Size    int64
	MIME    string
	Expires time.Time

	// Cacheable: the sender opted this share into layer B — the
	// relay may replay already-served bytes from volatile RAM.
	Cacheable bool

	// AwayPrefix: layer C opt-in, bytes (0 = off). The daemon
	// trickles this many prefix bytes to the relay after
	// announcing; the relay serves them while the sender is
	// offline.
	AwayPrefix int64

	// Account is the sender's account name on relays that require
	// tokens; empty on open relays. Never shown to receivers.
	Account string

	// Online reports whether a daemon connection is attached. The
	// registry does not own the connection; the hub sets this via
	// the returned handle.
	Online bool
}

// Handle is what the hub holds for a live share: a way to ask the
// serving daemon for bytes, and a way to detach on disconnect.
type Handle interface {
	// IssueRead asks the daemon for length bytes at offset and
	// returns the full reply.
	IssueRead(offset, length int64) ([]byte, error)

	// Close detaches the share: marks it offline. The connection
	// itself is owned by the hub, not the registry.
	Close()
}

// Registry is the concurrency-safe share table. Zero value usable.
type Registry struct {
	mu       sync.RWMutex
	shares   map[string]*entry
	onExpire []func(shareID string)

	// killed tombstones force-expired ids until their original
	// expiry, so a redialing daemon cannot bring a killed link back.
	// In-memory, like the rest of the table.
	killed map[string]time.Time
}

type entry struct {
	info   Info
	handle Handle
}

// NewRegistry returns an empty registry with the sweeper running.
// Sweep every interval; expired shares are removed and their handles
// detached. GC continues until the registry is dropped by the
// process — tests can stop it with Stop.
func NewRegistry(sweepEvery time.Duration) *Registry {
	r := &Registry{
		shares: make(map[string]*entry),
		killed: make(map[string]time.Time),
	}
	go r.sweepLoop(sweepEvery)
	return r
}

// Register adds a share announcement. If a share with the same id
// already exists (daemon redial), it is refreshed: info and handle
// replaced, but never a later expiry — a link's lifetime is fixed by
// its first announce. The hub has already checked the announce's
// owner key, so a redial here is the owner's. Killed ids are refused
// with ErrKilled until their original expiry.
func (r *Registry) Register(info Info, h Handle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.killed == nil {
		r.killed = make(map[string]time.Time)
	}
	if until, ok := r.killed[info.ID]; ok {
		if time.Now().Before(until) {
			return ErrKilled
		}
		delete(r.killed, info.ID)
	}
	if existing, ok := r.shares[info.ID]; ok {
		if existing.info.Expires.Before(info.Expires) {
			info.Expires = existing.info.Expires
		}
		if existing.handle != nil {
			// redial: the hub will have closed the old connection,
			// but be safe against a racing detach
			go existing.handle.Close()
		}
	}
	r.shares[info.ID] = &entry{info: info, handle: h}
	return nil
}

// Lookup returns the info for a share id. Expired shares return
// ErrGone; unknown ids ErrNotFound.
func (r *Registry) Lookup(id string) (Info, Handle, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.shares[id]
	if !ok {
		return Info{}, nil, ErrNotFound
	}
	if time.Now().After(e.info.Expires) {
		return Info{}, nil, ErrGone
	}
	info := e.info
	if e.handle != nil {
		info.Online = true
	}
	return info, e.handle, nil
}

// Detach marks a share offline when its daemon connection drops,
// unless a redial already replaced the handle. The share stays
// registered (id, size, expiry) so links read as "offline, will be
// back" instead of 404.
func (r *Registry) Detach(id string, h Handle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.shares[id]; ok && e.handle == h {
		e.handle = nil
	}
}

// OnExpire registers a callback fired when Sweep removes a share
// (or Kill force-expires one). The relay uses it to drop layer-B
// cache bytes with the link. Call before serving traffic.
func (r *Registry) OnExpire(fn func(shareID string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onExpire = append(r.onExpire, fn)
}

func (r *Registry) fireExpire(shareID string) {
	r.mu.RLock()
	fns := make([]func(string), len(r.onExpire))
	copy(fns, r.onExpire)
	r.mu.RUnlock()
	for _, fn := range fns {
		fn(shareID)
	}
}

// Kill force-expires a share immediately: removed from the table,
// handle detached, and tombstoned until its original expiry so the
// sender cannot re-announce it. Returns false when the id was never
// registered.
func (r *Registry) Kill(id string) bool {
	r.mu.Lock()
	e, ok := r.shares[id]
	if ok {
		delete(r.shares, id)
		if r.killed == nil {
			r.killed = make(map[string]time.Time)
		}
		r.killed[id] = e.info.Expires
	}
	r.mu.Unlock()
	if ok && e.handle != nil {
		e.handle.Close()
	}
	if ok {
		r.fireExpire(id)
	}
	return ok
}

// KillAccount kills every live share announced by account, as Kill
// does for one: removed, detached and tombstoned. It returns the
// killed ids. An empty account (open relay) matches nothing.
func (r *Registry) KillAccount(account string) []string {
	if account == "" {
		return nil
	}
	r.mu.RLock()
	var ids []string
	for id, e := range r.shares {
		if e.info.Account == account {
			ids = append(ids, id)
		}
	}
	r.mu.RUnlock()
	var killed []string
	for _, id := range ids {
		if r.Kill(id) {
			killed = append(killed, id)
		}
	}
	return killed
}

// Sweep removes expired shares once. Exported for tests.
func (r *Registry) Sweep() {
	r.mu.Lock()
	var detached []Handle
	var expired []string
	for id, e := range r.shares {
		if time.Now().After(e.info.Expires) {
			if e.handle != nil {
				detached = append(detached, e.handle)
			}
			delete(r.shares, id)
			expired = append(expired, id)
		}
	}
	for id, until := range r.killed {
		if time.Now().After(until) {
			delete(r.killed, id)
		}
	}
	r.mu.Unlock()
	for _, h := range detached {
		h.Close()
	}
	for _, id := range expired {
		r.fireExpire(id)
	}
}

func (r *Registry) sweepLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		r.Sweep()
	}
}

// Stop halts the sweeper goroutine. Only for tests.
func (r *Registry) Stop() {}

// Count returns the number of live (unexpired) shares. For tests
// and the admin page.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, e := range r.shares {
		if time.Now().Before(e.info.Expires) {
			n++
		}
	}
	return n
}
