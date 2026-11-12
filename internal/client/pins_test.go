package client_test

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/host/hosttest"
	"github.com/pyjeebz/ephemera/internal/record"
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

// stack is a central server and one joined host serving TLS. tlsKey, when
// set, is the key the host's TLS certificate is made from instead of the
// key it joined with: a stand-in for something between CLI and host.
func stack(t *testing.T, tlsKey ed25519.PrivateKey) (*control.Server, string) {
	t.Helper()
	cs := &control.Server{Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"}}
	ts := httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)
	_, key, _ := ed25519.GenerateKey(nil)
	hs := &host.Server{Backend: &sandboxtest.Local{Root: t.TempDir()}, Name: "box", Key: key}
	var hts *httptest.Server
	if tlsKey == nil {
		hts = hosttest.Serve(t, hs)
	} else {
		cert, err := record.Certificate(tlsKey)
		if err != nil {
			t.Fatal(err)
		}
		hts = httptest.NewUnstartedServer(hs.Handler())
		hts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}
		hts.StartTLS()
		t.Cleanup(hts.Close)
	}
	code, err := cs.CreateJoinCode(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	hosttest.Connect(t, hs, ts.URL, code, api.HostHello{Name: "box", URL: hts.URL, Capacity: 5})
	return cs, ts.URL
}

func newClient(t *testing.T, server string) (*client.Client, *[]string) {
	t.Helper()
	pins, err := client.LoadPins(filepath.Join(t.TempDir(), "known_hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	c := &client.Client{Server: server, Token: "eph_good", Pins: pins,
		OnNewHost: func(name, fp string) { seen = append(seen, name+" "+fp) }}
	return c, &seen
}

func TestFirstUsePins(t *testing.T) {
	_, url := stack(t, nil)
	c, seen := newClient(t, url)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		sb, err := c.Create(ctx, api.Spec{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Connections(ctx, sb, 0); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(*seen) != 1 || !strings.HasPrefix((*seen)[0], "box SHA256:") {
		t.Errorf("new-host notices = %v, want one for box", *seen)
	}
	info, err := os.Stat(c.Pins.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("pin file: %v, %v", info, err)
	}
	// A fresh client reading the same file trusts the host without asking.
	again, err := client.LoadPins(c.Pins.Path)
	if err != nil || len(again.IDs()) != 1 {
		t.Errorf("reloaded pins = %v, %v", again.IDs(), err)
	}
}

func TestChangedKeyRefused(t *testing.T) {
	_, url := stack(t, nil)
	c, _ := newClient(t, url)
	ctx := context.Background()
	sb, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	// Pin something else for this host, as if it had a different key before.
	if err := c.Pins.Set(sb.HostID, client.Pin{Name: "box", Fingerprint: "SHA256:somethingelse"}); err != nil {
		t.Fatal(err)
	}
	_, err = c.Connections(ctx, sb, 0)
	var kc *client.HostKeyChangedError
	if !errors.As(err, &kc) || !strings.Contains(err.Error(), "ephemera hosts trust box") {
		t.Fatalf("err = %v, want a key-changed error naming the fix", err)
	}
}

func TestStrictRefusesUnknownHosts(t *testing.T) {
	_, url := stack(t, nil)
	c, seen := newClient(t, url)
	c.Strict = true
	sb, err := c.Create(context.Background(), api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Connections(context.Background(), sb, 0); !errors.Is(err, client.ErrUnknownHost) {
		t.Fatalf("err = %v, want ErrUnknownHost", err)
	}
	if len(*seen) != 0 || len(c.Pins.IDs()) != 0 {
		t.Error("strict mode pinned a host")
	}
}

// A host whose TLS key isn't the one it joined with is refused, even on
// first use: the CLI checks the certificate against the server's record.
func TestTLSKeyMustMatchJoin(t *testing.T) {
	_, other, _ := ed25519.GenerateKey(nil)
	_, url := stack(t, other)
	c, _ := newClient(t, url)
	sb, err := c.Create(context.Background(), api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Connections(context.Background(), sb, 0)
	if err == nil || !strings.Contains(err.Error(), "not") || !strings.Contains(err.Error(), "the key it joined with") {
		t.Fatalf("err = %v, want the TLS key refused", err)
	}
}
