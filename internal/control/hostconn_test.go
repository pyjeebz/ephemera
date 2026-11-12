package control_test

import (
	"context"
	"crypto/ed25519"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/host/hosttest"
	"github.com/pyjeebz/ephemera/internal/record"
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

func newHost(t *testing.T, name string) (*host.Server, *sandboxtest.Local) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	be := &sandboxtest.Local{Root: t.TempDir()}
	return &host.Server{Backend: be, Name: name, Key: key}, be
}

// The central server sends work down the host's own connection: a host
// whose address leads nowhere still gets sandboxes made and destroyed.
func TestServerNeverDialsHost(t *testing.T) {
	cs := &control.Server{Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"}, HostSecret: "s"}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	hs, be := newHost(t, "behind-nat")
	// 192.0.2.0/24 is reserved for documentation: nothing answers there.
	hosttest.Connect(t, hs, ts.URL, "s", api.HostHello{Name: "behind-nat", URL: "http://192.0.2.1:7701", Capacity: 5})

	ctx := context.Background()
	c := &client.Client{Server: ts.URL, Token: "eph_good"}
	sb, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if be.Live() != 1 {
		t.Fatalf("host runs %d sandboxes, want 1", be.Live())
	}
	sr, err := c.Destroy(ctx, sb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if be.Live() != 0 {
		t.Error("destroy did not reach the host")
	}
	if sr == nil {
		t.Fatal("destroy returned no record")
	}
	rec, err := record.Verify(*sr)
	if err != nil {
		t.Fatalf("the record that came back over the connection doesn't verify: %v", err)
	}
	if rec.Sandbox != sb.ID || rec.HostKey != record.PublicKey(hs.Key) {
		t.Errorf("record is for %s, signed by %s", rec.Sandbox, rec.HostKey)
	}
}

// A host started before its central server keeps trying, and connects
// once the server is up.
func TestHostReconnects(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here yet

	hs, _ := newHost(t, "early")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	welcomed := make(chan struct{}, 1)
	go hs.Connect(ctx, host.ConnectConfig{ServerURL: "http://" + addr, Secret: "s",
		Hello:     api.HostHello{Name: "early", URL: "http://192.0.2.1:7701"},
		OnWelcome: func() { welcomed <- struct{}{} }})

	time.Sleep(1500 * time.Millisecond) // at least one failed try
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("port %s was taken meanwhile: %v", addr, err)
	}
	cs := &control.Server{Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"}, HostSecret: "s"}
	srv := &http.Server{Handler: cs.Handler()}
	go srv.Serve(ln)
	defer srv.Close()

	select {
	case <-welcomed:
	case <-time.After(10 * time.Second):
		t.Fatal("host never connected once the server was up")
	}
}

// A host with the wrong secret, or speaking another protocol version, is
// turned away.
func TestHostRefused(t *testing.T) {
	cs := &control.Server{Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"}, HostSecret: "s"}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/hosts/connect", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", api.HostUpgrade)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong secret: %s, want 401", resp.Status)
	}

	req.Header.Set("Authorization", "Bearer s")
	req.Header.Del("Upgrade")
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("no Upgrade header: %s, want 426", resp.Status)
	}
}
