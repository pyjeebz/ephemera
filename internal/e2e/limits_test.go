package e2e

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

// limitedStack is a central server and host where the user token has
// limits, on a clock the test moves. The returned beat sends a host
// heartbeat, as a real host does every few seconds; tests call it after
// moving the clock so the host does not look offline.
func limitedStack(t *testing.T, limits control.Limits) (*control.Server, *client.Client, *sandboxtest.Local, *fakeClock, func()) {
	t.Helper()
	clock := newClock()
	// Daily limits count by UTC day: start at midday so the minutes these
	// tests advance never cross midnight, whatever time they run.
	clock.ns.Store(time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC).UnixNano())
	cs := &control.Server{Tokens: control.StaticToken{Name: "agent", Token: userToken, Limits: limits},
		HostSecret: hostSecret, Log: discardLog(), Now: clock.now,
		StatePath: filepath.Join(t.TempDir(), "s.json")}
	cts := httptest.NewServer(cs.Handler())
	t.Cleanup(cts.Close)
	be := &sandboxtest.Local{Root: t.TempDir()}
	hs := &host.Server{Backend: be, Log: discardLog(), Name: "test-host", Key: testKey()}
	hts := httptest.NewServer(hs.Handler())
	t.Cleanup(hts.Close)
	t.Cleanup(func() { hs.DestroyAll(context.Background()) })
	beat := func() { heartbeat(t, hs, cts.URL, hts.URL) }
	beat()
	return cs, &client.Client{Server: cts.URL, Token: userToken}, be, clock, beat
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

func ctxFor(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
