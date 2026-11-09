package guest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/stream"
	"github.com/pyjeebz/ephemera/internal/worktree"
)

func newGuest(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := &Server{Workdir: t.TempDir(), Env: []string{"PATH=" + os.Getenv("PATH"), "BASE=1"}}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func execute(t *testing.T, ctx context.Context, url string, req api.ExecRequest) (stdout, stderr string, code int, failed string) {
	t.Helper()
	b, _ := json.Marshal(req)
	hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/exec", bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		return "", "", 0, err.Error()
	}
	defer resp.Body.Close()
	var out, errOut strings.Builder
	for {
		f, err := stream.Read(resp.Body)
		if err != nil {
			return out.String(), errOut.String(), 0, "stream ended: " + err.Error()
		}
		switch f.Kind {
		case stream.Stdout:
			out.Write(f.Data)
		case stream.Stderr:
			errOut.Write(f.Data)
		case stream.Exit:
			return out.String(), errOut.String(), f.ExitCode(), ""
		case stream.Error:
			return out.String(), errOut.String(), 0, string(f.Data)
		}
	}
}

func TestUploadExecDelete(t *testing.T) {
	s, ts := newGuest(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\necho \"hi $BASE $EXTRA\"; cat data/x.txt; echo err >&2; exit 5\n"), 0o755)
	os.MkdirAll(filepath.Join(src, "data"), 0o755)
	os.WriteFile(filepath.Join(src, "data/x.txt"), []byte("payload\n"), 0o644)
	var tarball bytes.Buffer
	if err := worktree.WriteTar(&tarball, src, []string{"data/x.txt", "run.sh"}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/files", &tarball)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("upload: %v %v", resp.Status, err)
	}

	out, errOut, code, failed := execute(t, context.Background(), ts.URL, api.ExecRequest{Argv: []string{"./run.sh"}, Env: []string{"EXTRA=2"}})
	if failed != "" || code != 5 || out != "hi 1 2\npayload\n" || errOut != "err\n" {
		t.Errorf("exec: out %q err %q code %d failed %q", out, errOut, code, failed)
	}

	_, _, code, _ = execute(t, context.Background(), ts.URL, api.ExecRequest{Argv: []string{"no-such-program-xyz"}})
	if code != 127 {
		t.Errorf("missing program: code %d, want 127", code)
	}

	b, _ := json.Marshal(api.DeleteFilesRequest{Paths: []string{"data/x.txt", "never-existed"}})
	resp, err = http.Post(ts.URL+"/files/delete", "application/json", bytes.NewReader(b))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %v %v", resp.Status, err)
	}
	if _, err := os.Stat(filepath.Join(s.Workdir, "data/x.txt")); !os.IsNotExist(err) {
		t.Error("file not deleted")
	}
	b, _ = json.Marshal(api.DeleteFilesRequest{Paths: []string{"../escape"}})
	resp, _ = http.Post(ts.URL+"/files/delete", "application/json", bytes.NewReader(b))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unsafe delete: status %d", resp.StatusCode)
	}
}

func TestCancelKillsWholeProcessGroup(t *testing.T) {
	s, ts := newGuest(t)
	marker := filepath.Join(s.Workdir, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		_, _, _, failed := execute(t, ctx, ts.URL, api.ExecRequest{Argv: []string{"sh", "-c", "sleep 300 & echo $! > child.pid; wait"}})
		done <- failed
	}()
	var pid string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(marker); err == nil && len(bytes.TrimSpace(b)) > 0 {
			pid = string(bytes.TrimSpace(b))
			break
		}
	}
	if pid == "" {
		t.Fatal("child never started")
	}
	cancel()
	<-done
	// The background sleep, a grandchild, must be gone too.
	for deadline := time.Now().Add(7 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		stat, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil || strings.Contains(string(stat), ") Z ") {
			return
		}
	}
	t.Errorf("background process %s survived the cancelled command", pid)
}

func TestPortUpgrade(t *testing.T) {
	_, ts := newGuest(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		line, _ := bufio.NewReader(c).ReadString('\n')
		io.WriteString(c, "pong "+line)
		c.Close()
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Send the request and the first bytes of the tunnelled stream
	// together, as a fast client would.
	io.WriteString(conn, "GET /ports/"+itoa(port)+" HTTP/1.1\r\nHost: guest\r\nConnection: Upgrade\r\nUpgrade: "+api.PortUpgrade+"\r\n\r\nping\n")
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("status = %q", status)
	}
	for {
		l, _ := br.ReadString('\n')
		if l == "\r\n" || l == "" {
			break
		}
	}
	reply, _ := br.ReadString('\n')
	if reply != "pong ping\n" {
		t.Errorf("reply = %q", reply)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
