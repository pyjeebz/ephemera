// Package hosttest connects host agents to central servers in tests.
package hosttest

import (
	"context"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/host"
)

// Connect connects hs to the central server at serverURL and waits until
// the server has welcomed it. The connection closes when the test ends;
// the returned func closes it sooner and waits until the host has let go.
func Connect(t testing.TB, hs *host.Server, serverURL, secret string, hello api.HostHello) (disconnect func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	welcomed := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		hs.Connect(ctx, host.ConnectConfig{ServerURL: serverURL, Secret: secret, Hello: hello,
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
