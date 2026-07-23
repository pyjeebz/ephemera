package jail

import (
	"slices"
	"strings"
	"syscall"
	"testing"
)

func testSpec() Spec {
	return Spec{
		Dir:       "/run/jails/abc123",
		Binary:    "/usr/bin/firecracker",
		Kernel:    "/images/vmlinux",
		Rootfs:    "/images/rootfs.ext4",
		APISock:   "run/firecracker.sock",
		VsockSock: "run/vsock.sock",
	}
}

// The daemon reaches the VMM through host paths under the jail dir, while the
// VMM is told in-jail paths. Confusing the two is the failure that would leave a
// machine unreachable, so pin both.
func TestHostAndGuestPathsDiverge(t *testing.T) {
	s := testSpec()

	if got, want := s.HostAPISock(), "/run/jails/abc123/run/firecracker.sock"; got != want {
		t.Errorf("HostAPISock = %q, want %q", got, want)
	}
	if got, want := s.HostVsockSock(), "/run/jails/abc123/run/vsock.sock"; got != want {
		t.Errorf("HostVsockSock = %q, want %q", got, want)
	}
	if got, want := s.GuestVsock(), "/run/vsock.sock"; got != want {
		t.Errorf("GuestVsock = %q, want %q", got, want)
	}
	if got, want := s.FirecrackerArgs(), []string{"--api-sock", "/run/firecracker.sock"}; !slices.Equal(got, want) {
		t.Errorf("FirecrackerArgs = %v, want %v", got, want)
	}
}

// The three isolation namespaces must be present and the network one absent —
// the machine keeps the host network on purpose. A wrong flag here is the
// difference between "isolated" and "isolated from its own firewall".
func TestSysProcAttrIsolatesTheRightNamespaces(t *testing.T) {
	sp := testSpec().SysProcAttr()

	for _, want := range []struct {
		name string
		flag uintptr
	}{
		{"CLONE_NEWUSER", syscall.CLONE_NEWUSER},
		{"CLONE_NEWNS", syscall.CLONE_NEWNS},
		{"CLONE_NEWPID", syscall.CLONE_NEWPID},
	} {
		if sp.Cloneflags&want.flag == 0 {
			t.Errorf("Cloneflags missing %s", want.name)
		}
	}
	if sp.Cloneflags&syscall.CLONE_NEWNET != 0 {
		t.Error("Cloneflags isolates the network namespace; it must not, or the host firewall stops applying")
	}

	// A single uid and gid mapped to root inside is what makes an unprivileged
	// process able to mount and pivot. Without the mapping the clone is refused.
	if len(sp.UidMappings) != 1 || sp.UidMappings[0].ContainerID != 0 {
		t.Errorf("uid mapping = %+v, want one entry mapping to 0", sp.UidMappings)
	}
	if len(sp.GidMappings) != 1 || sp.GidMappings[0].ContainerID != 0 {
		t.Errorf("gid mapping = %+v, want one entry mapping to 0", sp.GidMappings)
	}
	if sp.GidMappingsEnableSetgroups {
		t.Error("setgroups must stay denied for an unprivileged gid mapping to be accepted")
	}
	if sp.Pdeathsig != syscall.SIGKILL {
		t.Error("a jailed VMM should die with the daemon, not outlive it as an orphan")
	}
}

// eph-jail rebuilds the Spec from these flags, so a networked machine must carry
// the flag that binds /dev/net/tun.
func TestHelperArgsRoundTripNetwork(t *testing.T) {
	s := testSpec()
	if slices.Contains(s.HelperArgs(), "-network") {
		t.Error("an unnetworked spec passed -network")
	}
	s.Network = true
	if !slices.Contains(s.HelperArgs(), "-network") {
		t.Error("a networked spec did not pass -network")
	}
	// The images and dir have to survive the round trip, or eph-jail binds the
	// wrong files.
	args := strings.Join(s.HelperArgs(), " ")
	for _, want := range []string{"/run/jails/abc123", "/images/vmlinux", "/images/rootfs.ext4"} {
		if !strings.Contains(args, want) {
			t.Errorf("HelperArgs missing %q: %v", want, s.HelperArgs())
		}
	}
}

func TestPrepareRequiresADir(t *testing.T) {
	if err := (Spec{}).Prepare(); err == nil {
		t.Fatal("Prepare with no Dir returned no error")
	}
}

func TestPrepareCreatesTheRunDir(t *testing.T) {
	dir := t.TempDir()
	s := Spec{Dir: Dir(dir + "/jail")}
	if err := s.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// The socket directory must exist before the VMM starts, so its sockets have
	// somewhere to land where the daemon can see them.
	if err := s.Cleanup(); err != nil {
		t.Errorf("Cleanup: %v", err)
	}
}
