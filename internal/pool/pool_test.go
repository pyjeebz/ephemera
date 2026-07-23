package pool

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/machine"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewRejectsAZeroSize(t *testing.T) {
	if _, err := New(machine.RestoreConfig{}, 0, nil); err == nil {
		t.Fatal("New accepted a size of 0")
	}
}

// Get on a closed pool must not hang or hand out a machine.
func TestGetOnAClosedPoolFails(t *testing.T) {
	// A restore that always fails keeps any real VM from booting, so the pool
	// never fills — which is fine, this test is only about the closed path.
	p, err := New(machine.RestoreConfig{Snapshot: machine.Snapshot{Jailed: true}}, 1, discard())
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := p.Get(ctx); err != ErrClosed {
		t.Fatalf("Get on a closed pool = %v, want ErrClosed", err)
	}
}

// Close must return even when the pool could never fill, i.e. it does not block
// waiting for forks that will never arrive.
func TestCloseIsSafeWhenForksFail(t *testing.T) {
	p, err := New(machine.RestoreConfig{Snapshot: machine.Snapshot{Jailed: true}}, 2, discard())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung")
	}
	// A second Close is a no-op, not a panic.
	p.Close()
}
