package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/router"
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

// perSandboxDial is the test backend, but each sandbox's port reaches a
// server of its own, as separate VMs would.
type perSandboxDial struct {
	*sandboxtest.Local
	mu      sync.Mutex
	order   []string
	servers []string
}

func (p *perSandboxDial) Create(ctx context.Context, id string, spec api.Spec) error {
	p.mu.Lock()
	p.order = append(p.order, id)
	p.mu.Unlock()
	return p.Local.Create(ctx, id, spec)
}

func (p *perSandboxDial) Dial(ctx context.Context, id string, _ int) (net.Conn, error) {
	p.mu.Lock()
	addr := ""
	for i, o := range p.order {
		if o == id && i < len(p.servers) {
			addr = p.servers[i]
		}
	}
	p.mu.Unlock()
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func TestDevSessionsShareAPortByName(t *testing.T) {
	var servers []string
	for _, name := range []string{"main", "feature"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, name) }))
		defer srv.Close()
		servers = append(servers, strings.TrimPrefix(srv.URL, "http://"))
	}
	be := &perSandboxDial{Local: &sandboxtest.Local{Root: t.TempDir()}, servers: servers}
	s := newStack(t, be, "unused")
	be.mu.Lock()
	be.order = nil // forget the sandbox newStack made to check the host
	be.mu.Unlock()

	sock := filepath.Join(t.TempDir(), "router.sock")
	rs := &router.Server{Socket: sock, ListenHosts: []string{"127.0.0.1"}}
	rctx, stopRouter := context.WithCancel(context.Background())
	defer stopRouter()
	go rs.Serve(rctx)
	rc := &router.Client{Socket: sock}
	for i := 0; i < 100 && !rc.Running(); i++ {
		time.Sleep(10 * time.Millisecond)
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	startDev := func(name string) (context.CancelFunc, <-chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		ev := newDevEvents()
		done := make(chan error, 1)
		go func() {
			_, err := s.client.Dev(ctx, client.DevOptions{
				Dir:    worktree(t, map[string]string{"x": name}),
				Exec:   api.ExecRequest{Argv: []string{"sleep", "60"}},
				Ports:  []client.PortMap{{Remote: port, Shared: true}},
				Router: rc, Name: name,
				Stdout: io.Discard, Stderr: io.Discard, Events: ev.handle,
			})
			done <- err
		}()
		if got := wait(t, ev.local, "forwarding"); got != fmt.Sprintf("%s.localhost:%d", name, port) {
			t.Errorf("address = %q", got)
		}
		return cancel, done
	}
	get := func(host string) string {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
		req.Host = host
		resp, err := (&http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
		if err != nil {
			return "error"
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	stopMain, mainDone := startDev("main")
	stopFeature, featureDone := startDev("feature")
	if got := get(fmt.Sprintf("main.localhost:%d", port)); got != "main" {
		t.Errorf("main.localhost = %q", got)
	}
	if got := get(fmt.Sprintf("feature.localhost:%d", port)); got != "feature" {
		t.Errorf("feature.localhost = %q", got)
	}
	if got := get(fmt.Sprintf("localhost:%d", port)); got != "feature" {
		t.Errorf("plain localhost = %q, want the newest session", got)
	}

	stopFeature()
	<-featureDone
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if get(fmt.Sprintf("localhost:%d", port)) == "main" {
			break
		}
	}
	if got := get(fmt.Sprintf("localhost:%d", port)); got != "main" {
		t.Errorf("after feature stopped, plain localhost = %q, want main", got)
	}
	stopMain()
	<-mainDone
	if routes := rs.Routes(); len(routes) != 0 {
		t.Errorf("routes left: %+v", routes)
	}
}
