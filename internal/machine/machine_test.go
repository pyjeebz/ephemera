package machine

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyjeebz/ephemera/internal/vmnet"
)

func TestBootArgsCarryTheFlagsFirecrackerNeeds(t *testing.T) {
	got := BootArgs("/sbin/eph-init", nil)

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
	if got := BootArgs("", nil); strings.Contains(got, "init=") {
		t.Errorf("boot args %q should not set init when none was asked for", got)
	}
}

// A machine with no link must not be told about one. This is the default and
// the strongest isolation the project offers, so it gets a test of its own.
func TestBootArgsHaveNoNetworkByDefault(t *testing.T) {
	if got := BootArgs(AgentInit, nil); strings.Contains(got, "ip=") {
		t.Errorf("boot args %q configured a network for a machine that has none", got)
	}
}

func TestBootArgsConfigureTheGuestAddress(t *testing.T) {
	pool, err := vmnet.NewPool(vmnet.DefaultPool)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := pool.Take("0f8bdebe")
	if err != nil {
		t.Fatal(err)
	}

	got := BootArgs(AgentInit, &NetArgs{Lease: lease, DNS: netip.MustParseAddr("9.9.9.9")})

	// The kernel parses this positionally and silently ignores a malformed
	// value, leaving a guest with no address and no clue why — so pin the
	// exact string rather than its parts.
	want := "ip=10.79.0.2::10.79.0.1:255.255.255.252:ephemera:eth0:off:9.9.9.9"
	if !strings.Contains(got, want) {
		t.Errorf("boot args\n  got  %q\n  want to contain %q", got, want)
	}
}

func TestDefaultDNSAppliesWhenUnset(t *testing.T) {
	var c Config
	c.applyDefaults()
	if c.DNS != DefaultDNS {
		t.Errorf("DNS = %v, want %v", c.DNS, DefaultDNS)
	}

	explicit := Config{DNS: netip.MustParseAddr("8.8.8.8")}
	explicit.applyDefaults()
	if explicit.DNS.String() != "8.8.8.8" {
		t.Errorf("defaults overwrote an explicit resolver: %v", explicit.DNS)
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
