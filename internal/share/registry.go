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

// Info is one registered share: the announcement a daemon made.
type Info struct {
	ID      string
	Name    string
	Size    int64
	MIME    string
	Expires time.Time

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
	mu         sync.RWMutex
	shares     map[string]*entry
	notify     chan struct{} // closed/restarted on sweep to wake waiters
	notifyOnce sync.Once
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
	}
	go r.sweepLoop(sweepEvery)
	return r
}

// Register adds a share announcement. If a share with the same id
// already exists (daemon redial), it is refreshed: expiry extended,
// handle replaced. The hub owns calling Detach on the old handle.
func (r *Registry) Register(info Info, h Handle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.shares[info.ID]; ok && existing.handle != nil {
		// redial: the hub will have closed the old connection, but
		// be safe against a racing detach
		go existing.handle.Close()
	}
	r.shares[info.ID] = &entry{info: info, handle: h}
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

// Sweep removes expired shares once. Exported for tests.
func (r *Registry) Sweep() {
	r.mu.Lock()
	var detached []Handle
	for id, e := range r.shares {
		if time.Now().After(e.info.Expires) {
			if e.handle != nil {
				detached = append(detached, e.handle)
			}
			delete(r.shares, id)
		}
	}
	r.mu.Unlock()
	for _, h := range detached {
		h.Close()
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
