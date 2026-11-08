package e2e

import (
	"context"
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
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

// centralServer starts a central server that saves its state to path.
func centralServer(t *testing.T, path string, now func() time.Time) (*control.Server, string) {
	t.Helper()
	cs := &control.Server{
		Tokens:     control.StaticToken{Name: "test", Token: userToken},
		HostSecret: hostSecret,
		StatePath:  path,
		Log:        discardLog(),
		Now:        now,
	}
	if err := cs.Load(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)
	return cs, ts.URL
}

// heartbeat sends one heartbeat from the host to the central server and
// waits for the host to act on the reply.
func heartbeat(hs *host.Server, serverURL, hostURL string) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		hs.Register(ctx, http.DefaultClient, serverURL, hostSecret,
			api.RegisterHostRequest{Name: "test-host", URL: hostURL, Capacity: 10}, time.Hour)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
}

func TestStateSurvivesRestartAndReconciles(t *testing.T) {
	ctx := context.Background()
	state := filepath.Join(t.TempDir(), "sandboxes.json")
	be := &sandboxtest.Local{Root: t.TempDir()}
	hs := &host.Server{Backend: be, Secret: hostSecret, DefaultImage: "unused", Log: discardLog()}
	hts := httptest.NewServer(hs.Handler())
	defer hts.Close()

	// First central server: create a sandbox.
	_, url1 := centralServer(t, state, nil)
	heartbeat(hs, url1, hts.URL)
	c1 := &client.Client{Server: url1, Token: userToken}
	sb, err := c1.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	// "Restart": a new central server loads the saved state. It still
	// knows the sandbox, so the host keeps it.
	_, url2 := centralServer(t, state, nil)
	c2 := &client.Client{Server: url2, Token: userToken}
	list, err := c2.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != sb.ID {
		t.Fatalf("after restart, list = %+v, %v", list, err)
	}
	if list[0].Token != "" {
		t.Error("saved state returned a sandbox token")
	}
	heartbeat(hs, url2, hts.URL)
	if be.Live() != 1 {
		t.Fatalf("known sandbox was destroyed after restart")
	}
	// And it can still be destroyed through the new server.
	if err := c2.Destroy(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	if be.Live() != 0 {
		t.Fatal("destroy did not reach the host")
	}

	// Lost state: a central server with no record of a running sandbox
	// tells the host to destroy it.
	sb2, err := c2.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	_, url3 := centralServer(t, filepath.Join(t.TempDir(), "empty.json"), nil)
	heartbeat(hs, url3, hts.URL)
	if be.Live() != 0 || hs.Live() != 0 {
		t.Errorf("unknown sandbox %s was not destroyed", sb2.ID)
	}
}

func TestRecordDroppedWhenHostLostSandbox(t *testing.T) {
	ctx := context.Background()
	be := &sandboxtest.Local{Root: t.TempDir()}
	hs := &host.Server{Backend: be, Secret: hostSecret, DefaultImage: "unused", Log: discardLog()}
	hts := httptest.NewServer(hs.Handler())
	defer hts.Close()

	clock := newClock()
	_, url := centralServer(t, filepath.Join(t.TempDir(), "s.json"), clock.now)
	heartbeat(hs, url, hts.URL)
	c := &client.Client{Server: url, Token: userToken}
	sb, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	// The host loses the sandbox (say, it was restarted). Within the grace
	// period the record stays; after it, the record is dropped.
	hs.DestroyAll(ctx)
	heartbeat(hs, url, hts.URL)
	if list, _ := c.List(ctx); len(list) != 1 {
		t.Fatalf("record dropped during the grace period")
	}
	clock.advance(3 * time.Minute)
	heartbeat(hs, url, hts.URL)
	if list, _ := c.List(ctx); len(list) != 0 {
		t.Errorf("stale record for %s kept", sb.ID)
	}
}

func TestDestroyWhenHostOffline(t *testing.T) {
	ctx := context.Background()
	be := &sandboxtest.Local{Root: t.TempDir()}
	hs := &host.Server{Backend: be, Secret: hostSecret, DefaultImage: "unused", Log: discardLog()}
	hts := httptest.NewServer(hs.Handler())

	clock := newClock()
	_, url := centralServer(t, filepath.Join(t.TempDir(), "s.json"), clock.now)
	heartbeat(hs, url, hts.URL)
	c := &client.Client{Server: url, Token: userToken}
	sb, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	hts.Close() // the host goes away
	clock.advance(time.Minute)
	if err := c.Destroy(ctx, sb.ID); err != nil {
		t.Fatalf("destroy with the host offline: %v", err)
	}
	if list, _ := c.List(ctx); len(list) != 0 {
		t.Error("record kept for a sandbox on an offline host")
	}
}

// fakeClock is a clock tests move forward; the server reads it from
// other goroutines.
type fakeClock struct{ ns atomic.Int64 }

func newClock() *fakeClock {
	c := &fakeClock{}
	c.ns.Store(time.Now().UnixNano())
	return c
}

func (c *fakeClock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *fakeClock) advance(d time.Duration) { c.ns.Add(int64(d)) }
