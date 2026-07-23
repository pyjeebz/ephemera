package machine

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
)

// These tests boot real microVMs. They need KVM, the firecracker binary, and
// built images, so they skip rather than fail where those are missing — the
// unit tests still run everywhere.
//
// Build the prerequisites with:
//
//	build/fetch-kernel.sh && build/build-rootfs.sh
func requireVM(t *testing.T) Config {
	t.Helper()
	if testing.Short() {
		t.Skip("boots a real VM; skipped with -short")
	}
	if _, err := exec.LookPath("firecracker"); err != nil {
		t.Skip("firecracker not on PATH")
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err != nil {
		t.Skipf("/dev/kvm not usable: %v", err)
	} else {
		_ = f.Close()
	}

	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
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

	return Config{
		KernelPath: kernel,
		RootfsPath: rootfs,
		RunDir:     t.TempDir(),
	}
}

func TestBootSelftestGuestExitsCleanly(t *testing.T) {
	cfg := requireVM(t)
	cfg.Init = "/sbin/eph-selftest"

	var console bytes.Buffer
	cfg.Console = &console

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	t.Cleanup(func() { _ = m.Destroy(context.Background()) })

	select {
	case <-m.Done():
	case <-ctx.Done():
		t.Fatalf("guest never exited; console:\n%s", console.String())
	}

	// The guest resets itself, which Firecracker reports as a clean exit — this
	// is what reboot=k on the cmdline buys us.
	if err := m.Wait(); err != nil {
		t.Errorf("guest exited with an error: %v\nconsole:\n%s", err, console.String())
	}
	if !strings.Contains(console.String(), "selftest OK") {
		t.Errorf("selftest did not pass; console:\n%s", console.String())
	}
}

func TestExecInGuest(t *testing.T) {
	cfg := requireVM(t)
	cfg.Init = AgentInit

	var console bytes.Buffer
	cfg.Console = &console

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() { _ = m.Destroy(context.Background()) }()

	if err := m.WaitAgent(ctx); err != nil {
		t.Fatalf("agent never came up: %v\nconsole:\n%s", err, console.String())
	}

	t.Run("stdout and exit status", func(t *testing.T) {
		var out bytes.Buffer
		code, err := m.Exec(ctx, []string{"echo", "hello"}, &out, nil)
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if code != 0 {
			t.Errorf("code = %d, want 0", code)
		}
		if strings.TrimSpace(out.String()) != "hello" {
			t.Errorf("stdout = %q, want %q", out.String(), "hello")
		}
	})

	t.Run("non-zero exit is a result", func(t *testing.T) {
		code, err := m.Exec(ctx, []string{"sh", "-c", "exit 7"}, nil, nil)
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if code != 7 {
			t.Errorf("code = %d, want 7", code)
		}
	})

	t.Run("stderr is separate from stdout", func(t *testing.T) {
		var out, errb bytes.Buffer
		if _, err := m.Exec(ctx, []string{"sh", "-c", "echo o; echo e >&2"}, &out, &errb); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if strings.TrimSpace(out.String()) != "o" || strings.TrimSpace(errb.String()) != "e" {
			t.Errorf("streams crossed: stdout=%q stderr=%q", out.String(), errb.String())
		}
	})

	t.Run("the guest has no network interface", func(t *testing.T) {
		// Phase 2 adds networking; until then a machine is reachable only
		// through vsock, and that is worth pinning down.
		var out bytes.Buffer
		if _, err := m.Exec(ctx, []string{"sh", "-c", "ip -o link | awk -F': ' '{print $2}'"}, &out, nil); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		for _, iface := range strings.Fields(out.String()) {
			if iface != "lo" {
				t.Errorf("unexpected interface %q in guest", iface)
			}
		}
	})

	t.Run("a missing binary is an error not an exit code", func(t *testing.T) {
		_, err := m.Exec(ctx, []string{"definitely-not-a-command"}, nil, nil)
		if err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestDestroyRemovesRuntimeState(t *testing.T) {
	cfg := requireVM(t)
	cfg.Init = AgentInit
	cfg.Console = &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if err := m.WaitAgent(ctx); err != nil {
		t.Fatalf("agent never came up: %v", err)
	}

	apiSock := filepath.Join(cfg.RunDir, m.ID+".sock")
	if err := m.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	for _, p := range []string{apiSock, m.VsockPath()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived Destroy", filepath.Base(p))
		}
	}
}

func TestSelfTerminatingGuestCleansUpWithoutDestroy(t *testing.T) {
	// The bug this pins: a guest that resets itself exits the VMM with nobody
	// calling Destroy, so cleanup has to happen on the exit path.
	cfg := requireVM(t)
	cfg.Init = "/sbin/eph-selftest"
	cfg.Console = &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	apiSock := filepath.Join(cfg.RunDir, m.ID+".sock")

	select {
	case <-m.Done():
	case <-ctx.Done():
		t.Fatal("guest never exited")
	}

	for _, p := range []string{apiSock, m.VsockPath()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s leaked after the guest exited on its own", filepath.Base(p))
		}
	}
}
