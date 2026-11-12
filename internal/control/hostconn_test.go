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
	cs := &control.Server{Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"}}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	hs, be := newHost(t, "behind-nat")
	// 192.0.2.0/24 is reserved for documentation: nothing answers there.
	hosttest.Connect(t, hs, ts.URL, joinCode(t, cs, ""), api.HostHello{Name: "behind-nat", URL: "http://192.0.2.1:7701", Capacity: 5})

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

// A host whose server is down keeps trying, and connects once the server
// is back.
func TestHostReconnects(t *testing.T) {
	hosts := &control.FileHosts{} // shared by both servers, like a database
	tokens := control.StaticToken{Name: "laptop", Token: "eph_good"}

	// Join through one server.
	first := httptest.NewServer((&control.Server{Tokens: tokens, Hosts: hosts}).Handler())
	code, err := (&control.Server{Hosts: hosts}).CreateJoinCode(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	hs, _ := newHost(t, "early")
	id, err := host.Join(context.Background(), first.URL, code, "early", hs.Key)
	first.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Then connect to an address where nothing listens yet.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	welcomed := make(chan struct{}, 1)
	go hs.Connect(ctx, host.ConnectConfig{ServerURL: "http://" + addr, HostID: id.HostID,
		Hello:     api.HostHello{URL: "http://192.0.2.1:7701"},
		OnWelcome: func() { welcomed <- struct{}{} }})

	time.Sleep(1500 * time.Millisecond) // at least one failed try
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("port %s was taken meanwhile: %v", addr, err)
	}
	srv := &http.Server{Handler: (&control.Server{Tokens: tokens, Hosts: hosts}).Handler()}
	go srv.Serve(ln)
	defer srv.Close()

	select {
	case <-welcomed:
	case <-time.After(10 * time.Second):
		t.Fatal("host never connected once the server was up")
	}
}

func joinCode(t *testing.T, cs *control.Server, account string) string {
	t.Helper()
	code, err := cs.CreateJoinCode(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// connectOnce tries one connection as host id and returns the server's
// first answer to the hello.
func connectOnce(t *testing.T, serverURL, id string, key ed25519.PrivateKey) api.HostMessage {
	t.Helper()
	hs := &host.Server{Backend: &sandboxtest.Local{Root: t.TempDir()}, Key: key}
	got := make(chan api.HostMessage, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go hs.Connect(ctx, host.ConnectConfig{ServerURL: serverURL, HostID: id,
		Hello:     api.HostHello{URL: "http://192.0.2.1:7701"},
		OnWelcome: func() { got <- api.HostMessage{Type: "welcome"} }})
	select {
	case m := <-got:
		return m
	case <-ctx.Done():
		return api.HostMessage{Type: "refused"}
	}
}

// Joining: a code works once, and a host signs in only with the key it
// joined with, until it is removed.
func TestJoinAndSignIn(t *testing.T) {
	cs := &control.Server{Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"}}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	ctx := context.Background()
	hs, _ := newHost(t, "h1")

	code := joinCode(t, cs, "")
	id, err := host.Join(ctx, ts.URL, code, "h1", hs.Key)
	if err != nil {
		t.Fatal(err)
	}
	if id.Account != control.DefaultAccount || id.HostID == "" {
		t.Errorf("joined as %+v", id)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	if _, err := host.Join(ctx, ts.URL, code, "h2", other); err == nil {
		t.Error("a join code worked twice")
	}
	if _, err := host.Join(ctx, ts.URL, joinCode(t, cs, ""), "H1", other); err == nil {
		t.Error("a second host named h1 joined the same account")
	}
	if _, err := host.Join(ctx, ts.URL, "ej_madeup", "h3", other); err == nil {
		t.Error("a made-up code worked")
	}

	if m := connectOnce(t, ts.URL, id.HostID, other); m.Type == "welcome" {
		t.Error("a host signed in with a key it didn't join with")
	}
	if m := connectOnce(t, ts.URL, id.HostID, hs.Key); m.Type != "welcome" {
		t.Fatal("the joined host could not sign in")
	}

	// Removing it from the account keeps it out.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/hosts/"+id.HostID, nil)
	req.Header.Set("Authorization", "Bearer eph_good")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove host: %s", resp.Status)
	}
	if m := connectOnce(t, ts.URL, id.HostID, hs.Key); m.Type == "welcome" {
		t.Error("a removed host signed in")
	}
}

// Sandboxes go only to the account's own hosts, and each account lists
// only its own.
func TestHostsBelongToAnAccount(t *testing.T) {
	tf := &control.TokenFile{Path: t.TempDir() + "/tokens.json"}
	ctx := context.Background()
	_, alice, _ := tf.CreateToken(ctx, "a", "alice", control.Limits{})
	_, bob, _ := tf.CreateToken(ctx, "b", "bob", control.Limits{})
	cs := &control.Server{Tokens: tf}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	hs, be := newHost(t, "alices")
	hosttest.Connect(t, hs, ts.URL, joinCode(t, cs, "alice"), api.HostHello{Name: "alices", URL: "http://192.0.2.1:7701", Capacity: 5})

	if hosts, _ := (&client.Client{Server: ts.URL, Token: bob}).Hosts(ctx); len(hosts) != 0 {
		t.Errorf("bob lists %d of alice's hosts", len(hosts))
	}
	if _, err := (&client.Client{Server: ts.URL, Token: bob}).Create(ctx, api.Spec{}); err == nil {
		t.Error("bob's sandbox was placed on alice's host")
	}
	if be.Live() != 0 {
		t.Fatal("alice's host runs a sandbox for bob")
	}
	hosts, _ := (&client.Client{Server: ts.URL, Token: alice}).Hosts(ctx)
	if len(hosts) != 1 || !hosts[0].Online || hosts[0].Name != "alices" {
		t.Errorf("alice's hosts = %+v", hosts)
	}
	if _, err := (&client.Client{Server: ts.URL, Token: alice}).Create(ctx, api.Spec{}); err != nil {
		t.Errorf("alice's sandbox: %v", err)
	}
	// Bob can't remove alice's host either.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/hosts/"+hosts[0].ID, nil)
	req.Header.Set("Authorization", "Bearer "+bob)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob removing alice's host: %s, want 404", resp.Status)
	}
}
