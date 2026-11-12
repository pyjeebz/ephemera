// Package hosttest connects host agents to central servers in tests.
package hosttest

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/host"
)

// Connect joins hs to the central server at serverURL with a join code, as
// a host named hello.Name, then connects it and waits until the server has
// welcomed it. The connection closes when the test ends; the returned func
// closes it sooner and waits until the host has let go.
func Connect(t testing.TB, hs *host.Server, serverURL, code string, hello api.HostHello) (disconnect func()) {
	t.Helper()
	id, err := host.Join(context.Background(), serverURL, code, hello.Name, hs.Key)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	return ConnectAs(t, hs, serverURL, id.HostID, hello)
}

// ConnectAs connects a host that already joined, as host id.
func ConnectAs(t testing.TB, hs *host.Server, serverURL, hostID string, hello api.HostHello) (disconnect func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	welcomed := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		hs.Connect(ctx, host.ConnectConfig{ServerURL: serverURL, HostID: hostID, Hello: hello,
			OnWelcome: func() {
				select {
				case welcomed <- struct{}{}:
				default:
				}
			}})
	}()
	disconnect = func() {
		cancel()
		<-done
	}
	t.Cleanup(disconnect)
	select {
	case <-welcomed:
	case <-time.After(10 * time.Second):
		t.Fatalf("host %s never connected to %s", hello.Name, serverURL)
	}
	return disconnect
}

// Serve starts hs's API over TLS, with its own certificate, the way
// ephemera-host serves it. It stops when the test ends.
func Serve(t testing.TB, hs *host.Server) *httptest.Server {
	t.Helper()
	cfg, err := hs.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(hs.Handler())
	ts.TLS = cfg
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}
