// Package pool keeps a few machines forked and waiting, so handing one out is
// instant rather than a fork away.
//
// A fork is already fast — tens of milliseconds — but a warm pool takes even
// that off the request path: the machines are forked ahead of time from one
// snapshot, sitting resumed with their agents up, and a request just takes one
// and triggers a background refork to replace it. The caller waits for a channel
// receive, not for a VM.
//
// Every machine in the pool is an identical fork of the same snapshot, which is
// exactly what makes them interchangeable: the pool does not know or care what a
// machine will be used for, only that it is booted, agent-ready, and isolated
// from its siblings.
package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/pyjeebz/ephemera/internal/machine"
)

// ErrClosed is returned by Get once the pool is shutting down.
var ErrClosed = errors.New("pool: closed")

// Pool maintains a target number of ready, forked machines.
type Pool struct {
	template machine.RestoreConfig // the snapshot and how to restore it
	size     int
	log      *slog.Logger

	ready chan *machine.Machine

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// New starts a pool that keeps size machines forked from template ready. Filling
// happens in the background, so New returns immediately; the first Get may wait
// for the first fork if it beats the fill.
func New(template machine.RestoreConfig, size int, log *slog.Logger) (*Pool, error) {
	if size < 1 {
		return nil, fmt.Errorf("pool: size must be at least 1, got %d", size)
	}
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		template: template,
		size:     size,
		log:      log,
		ready:    make(chan *machine.Machine, size),
		ctx:      ctx,
		cancel:   cancel,
	}
	for range size {
		p.spawn()
	}
	return p, nil
}

// Get returns a ready machine, refilling the pool behind it. It returns
// instantly when a machine is waiting, and otherwise blocks until one is forked,
// the passed context is done, or the pool closes.
//
// The returned machine belongs to the caller now: the pool has forgotten it, and
// the caller is responsible for destroying it.
func (p *Pool) Get(ctx context.Context) (*machine.Machine, error) {
	select {
	case m := <-p.ready:
		// A nil receive means Close drained and closed the channel — a receive
		// from a closed channel succeeds immediately with the zero value, so this
		// is how a closed pool looks from here, not a real machine.
		if m == nil {
			return nil, ErrClosed
		}
		p.spawn() // replace the one just taken
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.ctx.Done():
		return nil, ErrClosed
	}
}

// Ready reports how many machines are waiting right now — for tests and metrics,
// not for deciding anything, since it changes underneath you.
func (p *Pool) Ready() int { return len(p.ready) }

// Close shuts the pool down: no more forks are started, in-flight forks are
// discarded as they finish, and every waiting machine is destroyed. Call it when
// no Get is in flight.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	// Cancel first so in-flight forks self-destroy instead of joining the pool,
	// then wait them out before draining what already made it in.
	p.cancel()
	p.wg.Wait()
	close(p.ready)
	for m := range p.ready {
		_ = m.Destroy(context.Background())
	}
}

// spawn forks one machine in the background and delivers it to the ready channel,
// unless the pool closed first, in which case the fork is destroyed rather than
// handed out.
func (p *Pool) spawn() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.wg.Add(1)
	p.mu.Unlock()

	go func() {
		defer p.wg.Done()

		m, err := machine.Restore(p.ctx, p.template)
		if err != nil {
			// A cancelled context is an expected error during shutdown, not a
			// failure worth shouting about.
			if p.ctx.Err() == nil {
				p.log.Error("pool: fork failed", "err", err)
			}
			return
		}
		select {
		case p.ready <- m:
		case <-p.ctx.Done():
			_ = m.Destroy(context.Background())
		}
	}()
}
