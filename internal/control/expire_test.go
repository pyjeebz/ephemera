package control_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host/hosttest"
	"github.com/pyjeebz/ephemera/internal/record"
)

// A sandbox that reaches its maximum lifetime stops using its token's
// minutes and its one sandbox slot when its host destroys it, not an hour
// later when the host forgets the record. The run keeps the reason the
// host gave, and the CLI still collects its signed record.
func TestExpiredSandboxStopsCountingAtItsLifetime(t *testing.T) {
	ends := newEndings()
	cs := &control.Server{
		Tokens: control.StaticToken{Name: "laptop", Token: "eph_good",
			Limits: control.Limits{MaxSandboxes: 1}},
		State: ends,
	}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	hs, be := newHost(t, "h1")
	hurl := hosttest.Serve(t, hs)
	hosttest.Connect(t, hs, ts.URL, joinCode(t, cs, ""), api.HostHello{Name: "h1", URL: hurl.URL, Capacity: 5})
	ctx := context.Background()
	c := &client.Client{Server: ts.URL, Token: "eph_good"}
	usage := func() api.Usage {
		t.Helper()
		u, err := c.Usage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	sb, err := c.Create(ctx, api.Spec{MaxLifetimeSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); be.Live() != 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the sandbox outlived its lifetime")
		}
	}
	time.Sleep(100 * time.Millisecond) // the host signs the record, then says so
	if err := hs.Heartbeat(ctx); err != nil {
		t.Fatal(err) // answered in order, so the server has read the message by now
	}

	// The run ended when the host destroyed it, with the host's reason.
	if why, at, ok := ends.ended(sb.ID); !ok || why != control.EndExpired {
		t.Fatalf("expired sandbox ended as %q (ended: %v), want %q", why, ok, control.EndExpired)
	} else if at.Before(sb.CreatedAt) || time.Since(at) > time.Minute {
		t.Errorf("run ended at %v, want about %v", at, sb.CreatedAt)
	}
	if run, ok := ends.of(sb.ID); !ok || run == nil {
		t.Error("the run ended with nothing from its host")
	}

	// Its minutes stopped: they no longer grow while its record is kept.
	before := usage()
	time.Sleep(1200 * time.Millisecond)
	after := usage()
	if after.MinutesToday != before.MinutesToday {
		t.Errorf("the owner's minutes grew from %v to %v after the sandbox ended",
			before.MinutesToday, after.MinutesToday)
	}
	if after.MinutesToday > 5 {
		t.Errorf("the owner's minutes are %v for a one-second run", after.MinutesToday)
	}

	// Its slot is free, and the sandbox is no longer listed as running.
	second, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatalf("the expired sandbox still holds the token's one slot: %v", err)
	}
	if list, err := c.List(ctx); err != nil {
		t.Fatal(err)
	} else if len(list) != 1 || list[0].ID != second.ID {
		t.Errorf("running sandboxes are %v, want only %s", list, second.ID)
	}

	// The CLI still collects the signed record.
	sr, err := c.Destroy(ctx, sb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sr == nil {
		t.Fatal("destroy returned no record")
	}
	rec, err := record.Verify(*sr)
	if err != nil {
		t.Fatalf("the record that came back doesn't verify: %v", err)
	}
	if rec.Sandbox != sb.ID || rec.Ended != control.EndExpired {
		t.Errorf("record is for %s ending as %q, want %s ending as %q",
			rec.Sandbox, rec.Ended, sb.ID, control.EndExpired)
	}
	if got := ends.ends(sb.ID); got != 1 {
		t.Errorf("the run ended %d times, want once", got)
	}
}
