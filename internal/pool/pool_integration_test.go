package pool

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/jail"
	"github.com/pyjeebz/ephemera/internal/machine"
)

// This boots a base, snapshots it, and stands up a pool of forks — then shows
// the point of the pool: taking a machine is instant, because the fork already
// happened. It needs KVM, firecracker, built images, and unprivileged user
// namespaces, so it skips where those are missing.
func TestPoolServesReadyMachines(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real VMs; skipped with -short")
	}
	base, snap, helper := bootAndSnapshot(t)
	_ = base

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const size = 2
	p, err := New(machine.RestoreConfig{
		Snapshot:   snap,
		RunDir:     "run",
		JailHelper: helper,
	}, size, discard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	// Give the pool a moment to fork its machines, then confirm it is full.
	waitFor(t, 30*time.Second, func() bool { return p.Ready() == size })

	// Taking a ready machine should be effectively free — no fork on the path.
	started := time.Now()
	m, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	took := time.Since(started)
	defer func() { _ = m.Destroy(context.Background()) }()
	if took > 20*time.Millisecond {
		t.Errorf("Get took %s from a full pool; it should be near-instant", took)
	}
	t.Logf("Get from a warm pool in %s (a fork is ~20ms; a boot ~1s)", took.Round(time.Microsecond))

	// And it is a real, ready machine: its agent answers with no wait.
	var out bytes.Buffer
	if _, err := m.Exec(ctx, []string{"echo", "warm"}, &out, nil); err != nil {
		t.Fatalf("Exec on pooled machine: %v", err)
	}
	if strings.TrimSpace(out.String()) != "warm" {
		t.Errorf("pooled machine exec = %q, want warm", out.String())
	}

	// Taking one triggers a refill, so the pool returns to full.
	waitFor(t, 30*time.Second, func() bool { return p.Ready() == size })
}

// bootAndSnapshot boots a jailed base machine, snapshots it, destroys the base,
// and returns the snapshot and the jail helper path.
func bootAndSnapshot(t *testing.T) (*machine.Machine, machine.Snapshot, string) {
	t.Helper()
	cfg := requirePoolVM(t)
	helper := buildJailHelper(t)
	cfg.Init = machine.AgentInit
	cfg.Jail = true
	cfg.JailHelper = helper
	cfg.Console = &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	base, err := machine.Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot base: %v", err)
	}
	if err := base.WaitAgent(ctx); err != nil {
		t.Fatalf("base agent never came up: %v", err)
	}
	snap, err := base.Snapshot(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := base.Destroy(ctx); err != nil {
		t.Fatalf("Destroy base: %v", err)
	}
	return nil, snap, helper
}

func requirePoolVM(t *testing.T) machine.Config {
	t.Helper()
	if _, err := exec.LookPath("firecracker"); err != nil {
		t.Skip("firecracker not on PATH")
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err != nil {
		t.Skipf("/dev/kvm not usable: %v", err)
	} else {
		_ = f.Close()
	}
	if err := jail.Available(); err != nil {
		t.Skipf("jailing not available: %v", err)
	}

	root := repoRoot(t)
	kernel := filepath.Join(root, "build", "kernel", "vmlinux")
	rootfs := filepath.Join(root, "build", "rootfs", "rootfs.ext4")
	for _, p := range []string{kernel, rootfs} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("guest image missing (%s) — run build/build-rootfs.sh", filepath.Base(p))
		}
	}
	if resolved, err := filepath.EvalSymlinks(kernel); err == nil {
		kernel = resolved
	}
	return machine.Config{KernelPath: kernel, RootfsPath: rootfs, RunDir: "run"}
}

func buildJailHelper(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "eph-jail")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/eph-jail")
	cmd.Dir = repoRoot(t)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build eph-jail: %v\n%s", err, b)
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}
