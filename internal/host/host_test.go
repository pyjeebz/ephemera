package host

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
)

// flakyBackend is a sandbox backend whose Destroy fails failures times
// before it succeeds.
type flakyBackend struct {
	mu       sync.Mutex
	failures int
	destroys int
	live     map[string]bool
}

func (b *flakyBackend) Create(_ context.Context, id string, _ api.Spec) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.live == nil {
		b.live = map[string]bool{}
	}
	b.live[id] = true
	return nil
}

func (b *flakyBackend) CopyIn(context.Context, string, io.Reader) error { return nil }

func (b *flakyBackend) Exec(context.Context, string, api.ExecRequest, io.Writer, io.Writer) (int, error) {
	return 0, nil
}

func (b *flakyBackend) Dial(context.Context, string, int) (net.Conn, error) {
	return nil, errors.New("no ports in tests")
}

func (b *flakyBackend) Destroy(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.destroys++
	if b.failures > 0 {
		b.failures--
		return errors.New("destroy failed")
	}
	delete(b.live, id)
	return nil
}

// alive reports whether the backend still runs the sandbox.
func (b *flakyBackend) alive(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.live[id]
}

// allowDestroys stops Destroy from failing.
func (b *flakyBackend) allowDestroys() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
}

// destroyCount returns how often Destroy was called.
func (b *flakyBackend) destroyCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.destroys
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newServer(t *testing.T, be *flakyBackend, life time.Duration) *Server {
	t.Helper()
	retry, retryMax := expireRetryDelay, expireMaxRetryDelay
	expireRetryDelay, expireMaxRetryDelay = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { expireRetryDelay, expireMaxRetryDelay = retry, retryMax })
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Backend: be, Key: key, MaxLifetime: life, BackendName: "test", Log: quiet()}
}

func created(t *testing.T, s *Server, id string) {
	t.Helper()
	if err := s.Create(context.Background(), api.HostCreateRequest{
		ID: id, TokenHash: "hash", Spec: api.Spec{Image: "image"},
	}); err != nil {
		t.Fatal(err)
	}
}

// A destroy that fails once must not end lifetime enforcement: the host
// tries again, and the sandbox ends expired with a record to collect.
func TestExpireRetriesAFailedDestroy(t *testing.T) {
	be := &flakyBackend{failures: 1}
	s := newServer(t, be, 50*time.Millisecond)
	created(t, s, "sb")

	for deadline := time.Now().Add(5 * time.Second); be.alive("sb"); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the sandbox outlived its lifetime after %d destroy attempts", be.destroyCount())
		}
	}
	if n := be.destroyCount(); n != 2 {
		t.Errorf("Destroy ran %d times, want 2: the failed attempt and its retry", n)
	}

	// The host signs the record after the backend reports the sandbox gone.
	var final *api.SignedRecord
	for deadline := time.Now().Add(5 * time.Second); final == nil; time.Sleep(5 * time.Millisecond) {
		s.mu.Lock()
		if e := s.sandboxes["sb"]; e != nil {
			final = e.final
		}
		s.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the expired sandbox left no signed record waiting for the CLI")
		}
	}
	var rec api.Record
	if err := json.Unmarshal(final.Record, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Ended != "expired" || !rec.DiskDeleted {
		t.Errorf("record says ended %q, disk deleted %v; want expired, true", rec.Ended, rec.DiskDeleted)
	}
}

// A destroy that keeps failing is tried again at the capped interval: the
// lifetime limit does not end after a fixed number of attempts, and the
// host keeps the sandbox in its table so its heartbeat still reports it.
func TestExpireKeepsTrying(t *testing.T) {
	be := &flakyBackend{failures: 1000}
	s := newServer(t, be, 50*time.Millisecond)
	created(t, s, "sb")

	waitFor := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); be.destroyCount() < n; time.Sleep(2 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("Destroy ran %d times, want %d", be.destroyCount(), n)
			}
		}
	}
	waitFor(5)
	if !s.live("sb") {
		t.Error("the host forgot a sandbox it could not destroy; its heartbeat no longer reports it")
	}
	waitFor(8)

	// Let the retry finish: a timer that is still armed at the end of the
	// test would read the delays after the test restored them.
	be.allowDestroys()
	for deadline := time.Now().Add(5 * time.Second); be.alive("sb"); time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the sandbox was not destroyed once Destroy stopped failing")
		}
	}
}

// A destroy that removes the sandbox but cannot sign its record must leave
// the entry retryable, not answer "already being destroyed" forever.
func TestDestroyRetriesAfterAFailedSignature(t *testing.T) {
	be := &flakyBackend{}
	s := &Server{Backend: be, BackendName: "test", Log: quiet()}
	created(t, s, "sb")

	_, err := s.Destroy(context.Background(), "sb")
	if !errors.Is(err, errUnsigned) || !strings.Contains(err.Error(), "host has no signing key") {
		t.Fatalf("destroy without a key: %v, want the signing key error", err)
	}
	if be.alive("sb") {
		t.Fatal("the sandbox was not destroyed")
	}
	_, err = s.Destroy(context.Background(), "sb")
	if !errors.Is(err, errUnsigned) {
		t.Errorf("second destroy: %v, want the signing key error again, not one that says a destroy is in progress", err)
	}
}

// An expiry destroy that succeeds but cannot sign its record must say so
// once and stop: the sandbox is gone, so no retry can produce the record.
func TestExpireStopsAfterAFailedSignature(t *testing.T) {
	be := &flakyBackend{}
	s := newServer(t, be, 50*time.Millisecond)
	s.Key = nil
	created(t, s, "sb")

	for deadline := time.Now().Add(5 * time.Second); be.alive("sb"); time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the sandbox was not destroyed at its lifetime")
		}
	}
	time.Sleep(100 * time.Millisecond) // several retry intervals
	if n := be.destroyCount(); n != 1 {
		t.Errorf("Destroy ran %d times, want 1: a record that cannot be signed is not retried", n)
	}
}
