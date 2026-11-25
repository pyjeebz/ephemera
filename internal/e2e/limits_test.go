package e2e

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/host/hosttest"
	"github.com/pyjeebz/ephemera/internal/sandbox"
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

// limitedStack is a central server and host where the user token has
// limits, on a clock the test moves. The returned beat sends a host
// heartbeat, as a real host does every few seconds; tests call it after
// moving the clock so the host does not look offline.
func limitedStack(t *testing.T, limits control.Limits) (*control.Server, *client.Client, *sandboxtest.Local, *fakeClock, func()) {
	t.Helper()
	be := &sandboxtest.Local{Root: t.TempDir()}
	cs, c, clock, beat := limitedStackOn(t, limits, be)
	return cs, c, be, clock, beat
}

// limitedStackOn is limitedStack with the sandbox backend the test picks,
// for a host whose backend misbehaves on purpose.
func limitedStackOn(t *testing.T, limits control.Limits, be sandbox.Backend) (*control.Server, *client.Client, *fakeClock, func()) {
	t.Helper()
	clock := newClock()
	// Daily limits count by UTC day: start at midday so the minutes these
	// tests advance never cross midnight, whatever time they run.
	clock.ns.Store(time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC).UnixNano())
	cs := &control.Server{Tokens: control.StaticToken{Name: "agent", Token: userToken, Limits: limits}, Log: discardLog(), Now: clock.now,
		StatePath: filepath.Join(t.TempDir(), "s.json")}
	cts := httptest.NewServer(cs.Handler())
	t.Cleanup(cts.Close)
	servers.Store(cts.URL, cs)
	hs := &host.Server{Backend: be, Log: discardLog(), Name: "test-host", Key: testKey()}
	hts := hosttest.Serve(t, hs)
	t.Cleanup(func() { hs.DestroyAll(context.Background()) })
	beat := func() { heartbeat(t, hs, cts.URL, hts.URL) }
	beat()
	return cs, &client.Client{Server: cts.URL, Token: userToken}, clock, beat
}

func tooMany(err error) bool {
	var se *client.StatusError
	return errors.As(err, &se) && se.Code == http.StatusTooManyRequests
}

func TestMaxSandboxesAtOnce(t *testing.T) {
	_, c, _, _, _ := limitedStack(t, control.Limits{MaxSandboxes: 2})
	ctx := context.Background()
	a, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(ctx, api.Spec{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(ctx, api.Spec{}); !tooMany(err) {
		t.Fatalf("third sandbox: %v, want 429", err)
	}
	c.Destroy(ctx, a.ID)
	if _, err := c.Create(ctx, api.Spec{}); err != nil {
		t.Errorf("after destroying one: %v", err)
	}
	u, _ := c.Usage(ctx)
	if u.Running != 2 || u.MaxSandboxes != 2 {
		t.Errorf("usage = %+v", u)
	}
}

func TestDailyMinutes(t *testing.T) {
	cs, c, be, clock, beat := limitedStack(t, control.Limits{MaxMinutesPerDay: 10})
	ctx := context.Background()

	// A sandbox's lifetime is capped at the minutes left today.
	sb, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if sb.Spec.MaxLifetimeSeconds != 600 {
		t.Errorf("lifetime = %ds, want 600", sb.Spec.MaxLifetimeSeconds)
	}

	// Four minutes of use is counted, running or not.
	clock.advance(4 * time.Minute)
	beat()
	if u, _ := c.Usage(ctx); u.MinutesToday < 3.9 || u.MinutesToday > 4.1 {
		t.Errorf("minutes today = %.2f, want about 4", u.MinutesToday)
	}
	c.Destroy(ctx, sb.ID)
	sb2, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if sb2.Spec.MaxLifetimeSeconds > 361 || sb2.Spec.MaxLifetimeSeconds < 359 {
		t.Errorf("second lifetime = %ds, want about 360", sb2.Spec.MaxLifetimeSeconds)
	}

	// Past the limit, the enforcer stops what runs and creates are refused.
	clock.advance(7 * time.Minute)
	beat()
	cs.Enforce(ctxFor(t, 100*time.Millisecond), 10*time.Millisecond)
	if be.Live() != 0 {
		t.Error("the enforcer left a sandbox running past the daily limit")
	}
	if _, err := c.Create(ctx, api.Spec{}); !tooMany(err) {
		t.Errorf("create over the limit: %v, want 429", err)
	}

	// A new day starts afresh.
	clock.advance(24 * time.Hour)
	beat()
	if _, err := c.Create(ctx, api.Spec{}); err != nil {
		t.Errorf("create the next day: %v", err)
	}
}

// stuckBackend is a sandbox backend that refuses to destroy a sandbox while
// broken is set, as a host whose kill times out. The host stays connected.
type stuckBackend struct {
	*sandboxtest.Local
	broken atomic.Bool
}

func (b *stuckBackend) Destroy(ctx context.Context, id string) error {
	if b.broken.Load() {
		return errors.New("the sandbox will not stop")
	}
	return b.Local.Destroy(ctx, id)
}

func TestDailyLimitWhenTheHostCannotDestroy(t *testing.T) {
	be := &stuckBackend{Local: &sandboxtest.Local{Root: t.TempDir()}}
	cs, c, clock, beat := limitedStackOn(t, control.Limits{MaxMinutesPerDay: 10}, be)
	ctx := context.Background()

	sb, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(11 * time.Minute)
	beat()

	// The enforcer tells the host to stop it; the host fails and keeps the
	// sandbox, so the run must stay: listed, running and counted.
	be.broken.Store(true)
	cs.Enforce(ctxFor(t, 100*time.Millisecond), 10*time.Millisecond)
	if be.Live() != 1 {
		t.Fatal("the test's backend destroyed the sandbox")
	}
	list, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != sb.ID {
		t.Errorf("listed sandboxes = %+v, want the one that still runs", list)
	}
	u, err := c.Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u.Running != 1 {
		t.Errorf("running = %d, want 1: a sandbox the host did not destroy is not ended", u.Running)
	}
	if u.MinutesToday < 10.9 || u.MinutesToday > 11.1 {
		t.Errorf("minutes today = %.2f, want about 11", u.MinutesToday)
	}

	// Once the host can destroy it, the enforcer ends the run at the limit.
	be.broken.Store(false)
	cs.Enforce(ctxFor(t, 100*time.Millisecond), 10*time.Millisecond)
	if be.Live() != 0 {
		t.Error("the enforcer left a sandbox running past the daily limit")
	}
	if u, _ := c.Usage(ctx); u.Running != 0 {
		t.Errorf("running = %d after the host destroyed it, want 0", u.Running)
	}
	if list, err := c.List(ctx); err != nil || len(list) != 0 {
		t.Errorf("listed sandboxes = %+v, %v, want none", list, err)
	}
}

func ctxFor(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
