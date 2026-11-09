package e2e

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/sandbox/firecracker"
)

// firecrackerBackend returns a backend on the guest image named by
// EPHEMERA_GUEST_IMAGE, or skips. It needs KVM and Firecracker.
func firecrackerBackend(t *testing.T) *firecracker.Backend {
	t.Helper()
	img := os.Getenv("EPHEMERA_GUEST_IMAGE")
	if img == "" {
		t.Skip("EPHEMERA_GUEST_IMAGE not set")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("no /dev/kvm")
	}
	if _, err := exec.LookPath("firecracker"); err != nil {
		t.Skip("firecracker not installed")
	}
	return &firecracker.Backend{ImageDir: img, StateDir: t.TempDir(), DefaultCPUs: 2, DefaultMemoryMB: 1024}
}

func TestFirecrackerRun(t *testing.T) {
	be := firecrackerBackend(t)
	s := newStack(t, be, "unused")
	dir := worktree(t, map[string]string{
		"hello.txt": "hello from the laptop\n",
		"check.sh": `#!/bin/sh
cat hello.txt
# The root filesystem is writable, on the VM's own disk.
touch /usr/local/bin/ephemera-was-here && echo root-writable
# There is no network at all.
python3 -c "import socket; socket.create_connection(('1.1.1.1', 80), timeout=3)" 2>/dev/null && echo online || echo offline
exit 7
`,
	})
	var stdout, stderr bytes.Buffer
	start := time.Now()
	res, err := s.client.Run(context.Background(), client.RunOptions{
		Dir:    dir,
		Exec:   api.ExecRequest{Argv: []string{"sh", "check.sh"}},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("%v\nstderr: %s", err, stderr.String())
	}
	t.Logf("ephemera run, end to end: %v", time.Since(start).Round(time.Millisecond))
	if res.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7 (stderr %q)", res.ExitCode, stderr.String())
	}
	if want := "hello from the laptop\nroot-writable\noffline\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if left, _ := os.ReadDir(be.StateDir); len(left) != 0 {
		t.Errorf("VM directory left behind: %v", left)
	}

	// A second sandbox starts clean: the first one's writes are gone.
	stdout.Reset()
	res, err = s.client.Run(context.Background(), client.RunOptions{
		Dir:    dir,
		Exec:   api.ExecRequest{Argv: []string{"sh", "-c", "test -e /usr/local/bin/ephemera-was-here && echo leaked || echo clean"}},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil || strings.TrimSpace(stdout.String()) != "clean" {
		t.Errorf("second sandbox: %q, %v", stdout.String(), err)
	}
}

func TestFirecrackerNetworkRefused(t *testing.T) {
	be := firecrackerBackend(t)
	s := newStack(t, be, "unused")
	_, err := s.client.Create(context.Background(), api.Spec{Network: api.NetworkOpen})
	if err == nil || !strings.Contains(err.Error(), "not available yet") {
		t.Errorf("err = %v, want a clear refusal", err)
	}
}

func TestFirecrackerDev(t *testing.T) {
	be := firecrackerBackend(t)
	s := newStack(t, be, "unused")
	dir := worktree(t, map[string]string{"index.html": "version one"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev := newDevEvents()
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := s.client.Dev(ctx, client.DevOptions{
			Dir: dir,
			// A real dev server, listening on the VM's localhost only.
			Exec:         api.ExecRequest{Argv: []string{"python3", "-m", "http.server", "8000", "--bind", "127.0.0.1"}},
			Ports:        []client.PortMap{{Local: 0, Remote: 8000}},
			SyncInterval: 100 * time.Millisecond,
			Stdout:       io.Discard,
			Stderr:       &stderr,
			Events:       ev.handle,
		})
		done <- err
	}()
	local := wait(t, ev.local, "port forwarding")
	hc := &http.Client{Timeout: 3 * time.Second}
	get := func(want string) {
		t.Helper()
		var last string
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			resp, err := hc.Get("http://" + local + "/index.html")
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
		t.Fatalf("GET = %q, want %q (server stderr: %s)", last, want, stderr.String())
	}
	get("version one")

	os.WriteFile(filepath.Join(dir, "index.html"), []byte("version two"), 0o644)
	wait(t, ev.synced, "sync of index.html")
	get("version two")

	cancel()
	if err := wait(t, done, "Dev to stop"); !errors.Is(err, context.Canceled) {
		t.Errorf("Dev error = %v", err)
	}
	if left, _ := os.ReadDir(be.StateDir); len(left) != 0 {
		t.Errorf("VM directory left behind: %v", left)
	}
}
