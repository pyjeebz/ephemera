package machine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootArgsCarryTheFlagsFirecrackerNeeds(t *testing.T) {
	got := BootArgs("/sbin/eph-init")

	// reboot=k is load-bearing: it routes guest resets through the i8042
	// controller, which is what makes Firecracker exit when the guest reboots.
	// Losing it turns a self-terminating machine into a hang.
	for _, want := range []string{"console=ttyS0", "reboot=k", "panic=1", "pci=off", "init=/sbin/eph-init"} {
		if !strings.Contains(got, want) {
			t.Errorf("boot args %q missing %q", got, want)
		}
	}
}

func TestBootArgsOmitInitWhenEmpty(t *testing.T) {
	if got := BootArgs(""); strings.Contains(got, "init=") {
		t.Errorf("boot args %q should not set init when none was asked for", got)
	}
}

func TestApplyDefaults(t *testing.T) {
	var c Config
	c.applyDefaults()

	if c.VCPUs != DefaultVCPUs {
		t.Errorf("VCPUs = %d, want %d", c.VCPUs, DefaultVCPUs)
	}
	if c.MemMiB != DefaultMemMiB {
		t.Errorf("MemMiB = %d, want %d", c.MemMiB, DefaultMemMiB)
	}
	if c.Init != DefaultInit {
		t.Errorf("Init = %q, want %q", c.Init, DefaultInit)
	}
	if c.RunDir == "" {
		t.Error("RunDir left empty")
	}
}

func TestApplyDefaultsKeepsCallerValues(t *testing.T) {
	c := Config{VCPUs: 4, MemMiB: 1024, Init: "/sbin/eph-selftest", RunDir: "/tmp/x"}
	c.applyDefaults()

	if c.VCPUs != 4 || c.MemMiB != 1024 || c.Init != "/sbin/eph-selftest" || c.RunDir != "/tmp/x" {
		t.Errorf("defaults overwrote explicit config: %+v", c)
	}
}

func TestValidateRequiresBothImages(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "present")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "absent")

	cases := map[string]struct {
		cfg  Config
		want string
	}{
		"no kernel":      {Config{RootfsPath: real}, "kernel"},
		"no rootfs":      {Config{KernelPath: real}, "rootfs"},
		"kernel missing": {Config{KernelPath: missing, RootfsPath: real}, "kernel"},
		"rootfs missing": {Config{KernelPath: real, RootfsPath: missing}, "rootfs"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.cfg.validate()
			if err == nil {
				t.Fatal("expected an error")
			}
			// The message should name which image is wrong — that is the whole
			// reason validation happens here rather than in the VMM.
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateAcceptsRealFiles(t *testing.T) {
	dir := t.TempDir()
	kernel := filepath.Join(dir, "vmlinux")
	rootfs := filepath.Join(dir, "rootfs.ext4")
	for _, p := range []string{kernel, rootfs} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := (Config{KernelPath: kernel, RootfsPath: rootfs}).validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestNewIDIsUniqueAndShort(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id, err := newID()
		if err != nil {
			t.Fatalf("newID: %v", err)
		}
		if len(id) != 8 {
			t.Fatalf("id %q is %d chars, want 8", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
