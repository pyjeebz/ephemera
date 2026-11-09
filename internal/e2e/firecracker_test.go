package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	be := &firecracker.Backend{ImageDir: img, StateDir: t.TempDir(), DefaultCPUs: 2, DefaultMemoryMB: 1024}
	// EPHEMERA_JAILED=1 runs the same tests with every VM jailed (as root).
	if os.Getenv("EPHEMERA_JAILED") == "1" {
		if os.Geteuid() != 0 {
			t.Skip("EPHEMERA_JAILED needs root")
		}
		be.Jailed = true
		be.ChrootBase = filepath.Join(filepath.Dir(img), fmt.Sprintf("jail-e2e-%d", os.Getpid()))
		t.Cleanup(func() { os.RemoveAll(be.ChrootBase) })
	}
	return be
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

// netRun runs a script in a fresh microVM sandbox with the given network
// settings and returns its output and connection log.
func netRun(t *testing.T, s *stack, spec api.Spec, script string) (string, []api.Connection) {
	t.Helper()
	var out bytes.Buffer
	res, err := s.client.Run(context.Background(), client.RunOptions{
		Dir:    worktree(t, map[string]string{"x": "x"}),
		Spec:   spec,
		Exec:   api.ExecRequest{Argv: []string{"sh", "-c", script}},
		Stdout: &out,
		Stderr: &out,
	})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	return out.String(), res.Connections
}

// status prints the HTTP status curl got for a URL through the sandbox's
// proxy settings, or "failed".
const statusOf = `st() { code=$(curl -s -o /dev/null -m 15 -w '%{http_code}' "$1") && echo "$1 $code" || echo "$1 failed"; }; `

func TestFirecrackerNetwork(t *testing.T) {
	be := firecrackerBackend(t)
	s := newStack(t, be, "unused")

	t.Run("none", func(t *testing.T) {
		out, conns := netRun(t, s, api.Spec{}, statusOf+
			`st https://example.com; curl -s -m 5 -x http://127.0.0.1:3128 https://example.com 2>&1 | head -c 200; echo; ls /sys/class/net`)
		t.Logf("\n%s", out)
		if !strings.Contains(out, "https://example.com failed") {
			t.Error("a sandbox with no network reached example.com")
		}
		if strings.Contains(out, "eth0") {
			t.Error("the VM has a network device")
		}
		if len(conns) != 0 {
			t.Errorf("connections logged with no network: %+v", conns)
		}
	})

	t.Run("allow list", func(t *testing.T) {
		out, conns := netRun(t, s, api.Spec{Network: api.NetworkAllow, Allow: []string{"example.com"}}, statusOf+
			`st https://example.com; st https://www.google.com; st http://example.com/`)
		t.Logf("\n%s", out)
		for _, want := range []string{"https://example.com 200", "https://www.google.com failed", "http://example.com/ 200"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q", want)
			}
		}
		var allowed, blocked []string
		for _, c := range conns {
			if c.Allowed {
				allowed = append(allowed, c.Host)
			} else {
				blocked = append(blocked, c.Host)
			}
		}
		t.Logf("allowed %v, blocked %v", allowed, blocked)
		if len(allowed) != 2 || len(blocked) != 1 || blocked[0] != "www.google.com" {
			t.Errorf("connection log: allowed %v, blocked %v", allowed, blocked)
		}
	})

	t.Run("open still blocks internal addresses", func(t *testing.T) {
		out, conns := netRun(t, s, api.Spec{Network: api.NetworkOpen}, statusOf+
			`st https://example.com; st http://metadata.google.internal/computeMetadata/v1/`)
		t.Logf("\n%s", out)
		if !strings.Contains(out, "https://example.com 200") {
			t.Error("open network could not reach example.com")
		}
		if strings.Contains(out, "metadata.google.internal/computeMetadata/v1/ 200") {
			t.Fatal("the sandbox reached the cloud metadata server")
		}
		found := false
		for _, c := range conns {
			if c.Host == "metadata.google.internal" {
				found = true
				if c.Allowed || !strings.Contains(c.Error, "not a public address") {
					t.Errorf("metadata record: %+v", c)
				}
			}
		}
		if !found {
			t.Error("the metadata attempt was not logged")
		}
	})
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

// TestFirecrackerTools checks the guest image's tools work without any
// network: Python with venv, git, Node with npm, and a C compiler.
func TestFirecrackerTools(t *testing.T) {
	be := firecrackerBackend(t)
	s := newStack(t, be, "unused")
	out, _ := netRun(t, s, api.Spec{}, `set -e
python3 -m venv /tmp/venv && /tmp/venv/bin/python -c "print('venv ok')"
python3 -m pip --version >/dev/null && echo "pip ok"
git init -q /tmp/repo && cd /tmp/repo && git -c user.email=a@b -c user.name=t commit -q --allow-empty -m first && git log --oneline | grep -q first && echo "git ok"
node -e "console.log('node ' + process.versions.node)"
cd /tmp && npm init -y >/dev/null && echo "npm ok"
printf 'int main(){return 0;}' > /tmp/a.c && cc /tmp/a.c -o /tmp/a && /tmp/a && echo "cc ok"`)
	t.Logf("\n%s", out)
	for _, want := range []string{"venv ok", "pip ok", "git ok", "node 24.", "npm ok", "cc ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
}
