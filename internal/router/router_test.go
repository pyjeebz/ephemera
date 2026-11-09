package router

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backend(t *testing.T, name string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s", name, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func start(t *testing.T, idle time.Duration) (*Server, *Client, <-chan struct{}) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "r.sock")
	s := &Server{Socket: sock, ListenHosts: []string{"127.0.0.1"}, IdleExit: idle}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { s.Serve(ctx); close(done) }()
	c := &Client{Socket: sock}
	for i := 0; i < 100 && !c.Running(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	return s, c, done
}

// get sends a request to the shared port with the given Host header.
func get(t *testing.T, port int, host string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/page", port), nil)
	req.Host = host
	resp, err := (&http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return "error: " + err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestRouting(t *testing.T) {
	_, c, _ := start(t, 0)
	port := freePort(t)
	bare := fmt.Sprintf("localhost:%d", port)
	ctx := context.Background()

	actx, stopA := context.WithCancel(ctx)
	ra, goneA, err := c.Hold(actx, "main", port, backend(t, "A"))
	if err != nil {
		t.Fatal(err)
	}
	if !ra.Focused || ra.Name != "main" {
		t.Errorf("first route: %+v", ra)
	}
	rb, _, err := c.Hold(ctx, "feature/Login", port, backend(t, "B"))
	if err != nil {
		t.Fatal(err)
	}
	if rb.Name != "feature-login" {
		t.Errorf("name = %q, want feature-login", rb.Name)
	}

	if got := get(t, port, fmt.Sprintf("main.localhost:%d", port)); got != "A /page" {
		t.Errorf("main.localhost: %q", got)
	}
	if got := get(t, port, fmt.Sprintf("feature-login.localhost:%d", port)); got != "B /page" {
		t.Errorf("feature-login.localhost: %q", got)
	}
	if got := get(t, port, bare); got != "B /page" {
		t.Errorf("plain localhost should reach the newest session: %q", got)
	}
	if err := c.Focus(ctx, "main", 0); err != nil {
		t.Fatal(err)
	}
	if got := get(t, port, bare); got != "A /page" {
		t.Errorf("after focus main: %q", got)
	}
	if err := c.Focus(ctx, "nope", 0); err == nil {
		t.Error("focus on an unknown name: want error")
	}

	// A second session under a taken name gets its own.
	rc, _, err := c.Hold(ctx, "main", port, backend(t, "C"))
	if err != nil || rc.Name != "main-2" {
		t.Errorf("duplicate name: %+v, %v", rc, err)
	}

	// When a session ends, its route goes.
	stopA()
	<-goneA
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if get(t, port, fmt.Sprintf("main.localhost:%d", port)) != "A /page" {
			break
		}
	}
	if got := get(t, port, fmt.Sprintf("main.localhost:%d", port)); strings.HasPrefix(got, "A") {
		t.Errorf("ended session still reachable: %q", got)
	}
	routes, _ := c.Routes(ctx)
	if len(routes) != 2 {
		t.Errorf("routes after one ended: %+v", routes)
	}
}

func TestNonHTTPGoesToFocused(t *testing.T) {
	_, c, _ := start(t, 0)
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				line, _ := bufio.NewReader(conn).ReadString('\n')
				io.WriteString(conn, "echo "+line)
				conn.Close()
			}()
		}
	}()
	port := freePort(t)
	if _, _, err := c.Hold(context.Background(), "db", port, echo.Addr().String()); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "ping\n")
	reply, _ := bufio.NewReader(conn).ReadString('\n')
	if reply != "echo ping\n" {
		t.Errorf("reply = %q", reply)
	}
}

func TestPortClosesAndRouterExitsWhenIdle(t *testing.T) {
	s, c, done := start(t, 200*time.Millisecond)
	port := freePort(t)
	ctx, stop := context.WithCancel(context.Background())
	_, gone, err := c.Hold(ctx, "x", port, backend(t, "X"))
	if err != nil {
		t.Fatal(err)
	}
	stop()
	<-gone
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("router did not exit when idle")
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
		t.Error("the shared port is still open")
	}
	if _, err := os.Stat(s.Socket); !os.IsNotExist(err) {
		t.Error("the socket was left behind")
	}
}

func TestLabel(t *testing.T) {
	for in, want := range map[string]string{"main": "main", "feature/Login": "feature-login", "  --x--": "x", "a_b.c": "a-b-c"} {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
}

// An address naming no session is an error, never another session's page.
func TestUnknownNameIsNotFound(t *testing.T) {
	_, c, _ := start(t, 0)
	port := freePort(t)
	if _, _, err := c.Hold(context.Background(), "main", port, backend(t, "A")); err != nil {
		t.Fatal(err)
	}
	if got := get(t, port, fmt.Sprintf("typo.localhost:%d", port)); !strings.Contains(got, "no dev session at typo.localhost") {
		t.Errorf("unknown name got %q", got)
	}
	if got := get(t, port, fmt.Sprintf("localhost:%d", port)); got != "A /page" {
		t.Errorf("plain localhost = %q", got)
	}
}
