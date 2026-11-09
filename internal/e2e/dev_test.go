package e2e

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/sandbox/docker"
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

// devEvents collects Dev's events for a test.
type devEvents struct {
	mu      sync.Mutex
	sandbox string
	local   chan string
	synced  chan map[string]any
}

func newDevEvents() *devEvents {
	return &devEvents{local: make(chan string, 4), synced: make(chan map[string]any, 64)}
}

func (d *devEvents) handle(ev string, f map[string]any) {
	switch ev {
	case "sandbox":
		d.mu.Lock()
		d.sandbox = f["id"].(string)
		d.mu.Unlock()
	case "forwarding":
		d.local <- f["local"].(string)
	case "synced":
		if f["initial"] != true {
			d.synced <- f
		}
	}
}

func (d *devEvents) id() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sandbox
}

func wait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// echoServer stands in for a dev server listening inside the sandbox.
func echoServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, "echo: "+line)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestDevForwardsPortsAndSyncsChanges(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	dir := worktree(t, map[string]string{"app.js": "v1", "old.txt": "remove me"})
	port := echoServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev := newDevEvents()
	done := make(chan error, 1)
	go func() {
		_, err := s.client.Dev(ctx, client.DevOptions{
			Dir:          dir,
			Exec:         api.ExecRequest{Argv: []string{"sleep", "60"}},
			Ports:        []client.PortMap{{Local: 0, Remote: port}},
			SyncInterval: 50 * time.Millisecond,
			Stdout:       io.Discard,
			Stderr:       io.Discard,
			Events:       ev.handle,
		})
		done <- err
	}()

	// Port forwarding: laptop port -> host -> sandbox port.
	local := wait(t, ev.local, "port forwarding")
	conn, err := net.Dial("tcp", local)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(conn, "ping\n")
	reply, _ := bufio.NewReader(conn).ReadString('\n')
	conn.Close()
	if reply != "echo: ping\n" {
		t.Errorf("forwarded reply = %q", reply)
	}

	// A changed file and a deleted file reach the sandbox.
	sandboxDir := filepath.Join(be.Root, ev.id())
	time.Sleep(20 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("v2 with more"), 0o644)
	os.Remove(filepath.Join(dir, "old.txt"))
	got := map[string]int{}
	for got["files"] < 1 || got["deleted"] < 1 {
		f := wait(t, ev.synced, "sync of changes")
		got["files"] += f["files"].(int)
		got["deleted"] += f["deleted"].(int)
	}
	if b, _ := os.ReadFile(filepath.Join(sandboxDir, "app.js")); string(b) != "v2 with more" {
		t.Errorf("sandbox app.js = %q", b)
	}
	if _, err := os.Stat(filepath.Join(sandboxDir, "old.txt")); !os.IsNotExist(err) {
		t.Error("old.txt was not deleted in the sandbox")
	}

	cancel()
	if err := wait(t, done, "Dev to stop"); !errors.Is(err, context.Canceled) {
		t.Errorf("Dev error = %v, want context.Canceled", err)
	}
	if be.Live() != 0 {
		t.Error("sandbox left behind after stopping")
	}
}

func TestDevPortInUseFailsBeforeCreating(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	_, err = s.client.Dev(context.Background(), client.DevOptions{
		Dir:   worktree(t, map[string]string{"a": "a"}),
		Exec:  api.ExecRequest{Argv: []string{"true"}},
		Ports: []client.PortMap{{Local: busy.Addr().(*net.TCPAddr).Port, Remote: 3000}},
	})
	if err == nil {
		t.Fatal("want an error for a port already in use")
	}
	if list, _ := s.client.List(context.Background()); len(list) != 0 {
		t.Error("a sandbox was created even though the port was taken")
	}
}

func TestParsePort(t *testing.T) {
	for in, want := range map[string]client.PortMap{"3000": {Local: 0, Remote: 3000, Shared: true}, "8080:3000": {Local: 8080, Remote: 3000}, "0:5173": {Local: 0, Remote: 5173}} {
		if got, err := client.ParsePort(in); err != nil || got != want {
			t.Errorf("ParsePort(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "x", "3000:", ":3000", "70000", "1:0"} {
		if _, err := client.ParsePort(bad); err == nil {
			t.Errorf("ParsePort(%q): want error", bad)
		}
	}
}

func TestDevDocker(t *testing.T) {
	if testing.Short() || !dockerAvailable() {
		t.Skip("docker not available")
	}
	const image = "alpine:3.20"
	if err := ensureImage(image); err != nil {
		t.Skip(err)
	}
	s := newStack(t, &docker.Backend{}, image)
	// A tiny HTTP server that serves msg.txt, bound to the sandbox's
	// localhost only, with networking off.
	server := `while true; do body="$(cat msg.txt)"; printf 'HTTP/1.1 200 OK\r\nContent-Length: %s\r\nConnection: close\r\n\r\n%s' "${#body}" "$body" | nc -l -s 127.0.0.1 -p 8080 >/dev/null; done`
	dir := worktree(t, map[string]string{"msg.txt": "version one"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev := newDevEvents()
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := s.client.Dev(ctx, client.DevOptions{
			Dir:          dir,
			Exec:         api.ExecRequest{Argv: []string{"sh", "-c", server}},
			Ports:        []client.PortMap{{Local: 0, Remote: 8080}},
			SyncInterval: 100 * time.Millisecond,
			Stdout:       io.Discard,
			Stderr:       &stderr,
			Events:       ev.handle,
		})
		done <- err
	}()
	local := wait(t, ev.local, "port forwarding")
	hc := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	get := func(want string) {
		t.Helper()
		var last string
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			resp, err := hc.Get("http://" + local + "/")
			if err != nil {
				last = err.Error()
				continue
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if last = string(b); last == want {
				return
			}
		}
		t.Fatalf("GET = %q, want %q (sandbox stderr: %s)", last, want, stderr.String())
	}
	get("version one")

	os.WriteFile(filepath.Join(dir, "msg.txt"), []byte("version two"), 0o644)
	wait(t, ev.synced, "sync of msg.txt")
	get("version two")

	id := ev.id()
	cancel()
	if err := wait(t, done, "Dev to stop"); !errors.Is(err, context.Canceled) {
		t.Errorf("Dev error = %v", err)
	}
	if strings.TrimSpace(dockerPS(id)) != "" {
		t.Errorf("container for %s still exists", id)
	}
}
