package e2e

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/host/hosttest"
	"github.com/pyjeebz/ephemera/internal/pgstore/pgtest"
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

// links are the open host connections, by host and server.
var links sync.Map // linkKey -> func() (disconnect)

type linkKey struct {
	hs  *host.Server
	url string
}

// heartbeat connects the host to the central server the first time it is
// called for that pair, and sends a heartbeat on every connection the host
// has after that. Either way, the server has reconciled the host's
// sandboxes, and the host has acted on the answer, when it returns.
func heartbeat(t *testing.T, hs *host.Server, serverURL, hostURL string) {
	t.Helper()
	key := linkKey{hs, serverURL}
	if _, ok := links.Load(key); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := hs.Heartbeat(ctx); err != nil {
			t.Fatal(err)
		}
		return
	}
	disconnect := hosttest.Connect(t, hs, serverURL, hostSecret, api.HostHello{Name: "test-host", URL: hostURL, Capacity: 10})
	links.Store(key, disconnect)
	t.Cleanup(func() { links.Delete(key) })
}

// disconnect closes the host's connection to the server and waits until
// the server lists the host as offline.
func disconnect(t *testing.T, hs *host.Server, serverURL string) {
	t.Helper()
	d, ok := links.LoadAndDelete(linkKey{hs, serverURL})
	if !ok {
		t.Fatal("host is not connected to that server")
	}
	d.(func())()
	c := &client.Client{Server: serverURL, Token: userToken}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if hosts, err := c.Hosts(context.Background()); err == nil && len(hosts) > 0 && !hosts[0].Online {
			return
		}
	}
	t.Fatal("server still lists the host as online")
}

func TestStateSurvivesRestartAndReconciles(t *testing.T) {
	ctx := context.Background()
	state := filepath.Join(t.TempDir(), "sandboxes.json")
	be := &sandboxtest.Local{Root: t.TempDir()}
	hs := &host.Server{Backend: be, DefaultImage: "unused", Log: discardLog(), Name: "test-host", Key: testKey()}
	hts := httptest.NewServer(hs.Handler())
	defer hts.Close()

	// First central server: create a sandbox.
	_, url1 := centralServer(t, state, nil)
	heartbeat(t, hs, url1, hts.URL)
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
	heartbeat(t, hs, url2, hts.URL)
	if be.Live() != 1 {
		t.Fatalf("known sandbox was destroyed after restart")
	}
	// And it can still be destroyed through the new server.
	if _, err := c2.Destroy(ctx, sb.ID); err != nil {
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
	heartbeat(t, hs, url3, hts.URL)
	if be.Live() != 0 || hs.Live() != 0 {
		t.Errorf("unknown sandbox %s was not destroyed", sb2.ID)
	}
}

// The same restart, with the state in Postgres: a second central server on
// the same database still knows the sandbox and can destroy it.
func TestStateSurvivesRestartOnPostgres(t *testing.T) {
	ctx := context.Background()
	db, _ := pgtest.Open(t)
	be := &sandboxtest.Local{Root: t.TempDir()}
	hs := &host.Server{Backend: be, DefaultImage: "unused", Log: discardLog(), Name: "test-host", Key: testKey()}
	hts := httptest.NewServer(hs.Handler())
	defer hts.Close()

	start := func() string {
		cs := &control.Server{Tokens: control.StaticToken{Name: "test", Token: userToken},
			HostSecret: hostSecret, State: db, Log: discardLog()}
		if err := cs.Load(); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(cs.Handler())
		t.Cleanup(ts.Close)
		return ts.URL
	}
	url1 := start()
	heartbeat(t, hs, url1, hts.URL)
	sb, err := (&client.Client{Server: url1, Token: userToken}).Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	url2 := start()
	c2 := &client.Client{Server: url2, Token: userToken}
	list, err := c2.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != sb.ID {
		t.Fatalf("after restart, list = %+v, %v", list, err)
	}
	heartbeat(t, hs, url2, hts.URL)
	if be.Live() != 1 {
		t.Fatal("known sandbox was destroyed after restart")
	}
	if _, err := c2.Destroy(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	if running, _ := db.RunningSandboxes(ctx); len(running) != 0 {
		t.Errorf("destroyed sandbox still running in the database: %+v", running)
	}
	used, _ := db.UsageSince(ctx, "2000-01-01")
	if len(used) == 0 {
		t.Error("no usage recorded for the destroyed sandbox")
	}
}

func TestRecordDroppedWhenHostLostSandbox(t *testing.T) {
	ctx := context.Background()
	be := &sandboxtest.Local{Root: t.TempDir()}
	hs := &host.Server{Backend: be, DefaultImage: "unused", Log: discardLog(), Name: "test-host", Key: testKey()}
	hts := httptest.NewServer(hs.Handler())
	defer hts.Close()

	clock := newClock()
	_, url := centralServer(t, filepath.Join(t.TempDir(), "s.json"), clock.now)
	heartbeat(t, hs, url, hts.URL)
	c := &client.Client{Server: url, Token: userToken}
	sb, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	// The host loses the sandbox (say, it was restarted). Within the grace
	// period the record stays; after it, the record is dropped.
	hs.DestroyAll(ctx)
	heartbeat(t, hs, url, hts.URL)
	if list, _ := c.List(ctx); len(list) != 1 {
		t.Fatalf("record dropped during the grace period")
	}
	clock.advance(3 * time.Minute)
	heartbeat(t, hs, url, hts.URL)
	if list, _ := c.List(ctx); len(list) != 0 {
		t.Errorf("stale record for %s kept", sb.ID)
	}
}

func TestDestroyWhenHostOffline(t *testing.T) {
	ctx := context.Background()
	be := &sandboxtest.Local{Root: t.TempDir()}
	hs := &host.Server{Backend: be, DefaultImage: "unused", Log: discardLog(), Name: "test-host", Key: testKey()}
	hts := httptest.NewServer(hs.Handler())

	clock := newClock()
	_, url := centralServer(t, filepath.Join(t.TempDir(), "s.json"), clock.now)
	heartbeat(t, hs, url, hts.URL)
	c := &client.Client{Server: url, Token: userToken}
	sb, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	// The host goes away.
	disconnect(t, hs, url)
	hts.Close()
	if _, err := c.Destroy(ctx, sb.ID); err != nil {
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
