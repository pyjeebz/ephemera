// Package jail confines a VMM to a minimal filesystem and its own process
// tree, without any privilege the daemon does not already have.
//
// It is a hand-rolled equivalent of Firecracker's jailer, with one deliberate
// difference: the jailer is a privileged program that drops down to safe, and
// this never has anything to drop. It leans on a user namespace instead. Inside
// a fresh user namespace an ordinary uid becomes root — but root only over that
// namespace, which is exactly enough to unshare a mount and pid namespace,
// build a chroot out of bind mounts, and pivot_root into it. Step outside and
// the process is still just the uid that started it, with no capability over
// anything real.
//
// What this gives a machine, on top of the network and cgroup limits it already
// has: it can see a handful of files and device nodes and nothing else of the
// host filesystem, and it cannot see, signal, or learn the pids of any process
// outside its own jail. Firecracker's own seccomp filters, on by default,
// remain the syscall boundary — the jail does not replace them.
//
// The mechanism has two halves that run in different processes. Spec.SysProcAttr
// builds the clone flags and id mappings the daemon uses to spawn the jail
// helper; Enter is what that helper runs, inside the new namespaces, to build
// the chroot and pivot into it before exec'ing the VMM. They are kept in one
// package so the two halves cannot drift.
package jail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Dir is where a machine's jail is built on the host: a real directory the VMM
// will pivot into. Sockets the VMM creates inside it stay visible here, because
// the directory is ordinary host storage — the mount namespace changes what the
// VMM can see, not where its files land.
type Dir string

// hostMounts are the paths the VMM needs from the host, bind-mounted into the
// jail. Everything else on the host filesystem simply is not there once we have
// pivoted.
type mount struct {
	source string // host path; empty means "make an empty dir/file to mount onto"
	target string // path inside the jail, without leading slash
	dev    bool   // a device node (bind a file) rather than a directory
	ro     bool
}

// Spec describes one machine's jail.
type Spec struct {
	Dir     Dir    // host directory to build the jail in
	Binary  string // firecracker binary on the host, bound in as /firecracker
	Kernel  string // host kernel path, bound in as /vmlinux
	Rootfs  string // host rootfs path, bound in as /rootfs.ext4
	Network bool   // bind /dev/net/tun so the VMM can open its interface

	// APISock and VsockSock are the socket paths *inside* the jail. Their host
	// locations are these joined onto Dir, which is how the daemon reaches them.
	APISock   string
	VsockSock string
}

// Guest paths — fixed names inside the jail, so the boot arguments and drive
// paths the daemon sends refer to files that are actually there.
const (
	GuestBinary = "/firecracker"
	GuestKernel = "/vmlinux"
	GuestRootfs = "/rootfs.ext4"
	guestRunDir = "run"
)

// HostAPISock is where the VMM's API socket lands on the host.
func (s Spec) HostAPISock() string { return filepath.Join(string(s.Dir), s.APISock) }

// HostVsockSock is where the VMM's vsock socket lands on the host.
func (s Spec) HostVsockSock() string { return filepath.Join(string(s.Dir), s.VsockSock) }

// SysProcAttr is how the daemon spawns the jail helper: in new user, mount, and
// pid namespaces, with our uid and gid mapped to root inside so the helper can
// mount and pivot. Deliberately not a new *network* namespace — the machine
// keeps the host's network so its TAP and the host firewall are unchanged, and
// the TAP is reachable because vmnet marked it owned by our uid.
func (s Spec) SysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		// setgroups must stay denied for an unprivileged gid mapping to be
		// allowed at all; we map a single gid and need nothing more.
		GidMappingsEnableSetgroups: false,
		// If the daemon dies, the jailed VMM should not outlive it as an orphan
		// in a namespace nobody is watching.
		Pdeathsig: syscall.SIGKILL,
	}
}

// Prepare creates the jail directory and the host-side socket directory before
// the VMM is spawned. The bind mounts themselves happen inside the namespace,
// in Enter, but the directory has to exist on the host first so its sockets are
// reachable.
func (s Spec) Prepare() error {
	if s.Dir == "" {
		return fmt.Errorf("jail: Dir is required")
	}
	runDir := filepath.Join(string(s.Dir), guestRunDir)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("jail: prepare %s: %w", runDir, err)
	}
	return nil
}

// Cleanup removes the jail directory. The bind mounts inside it lived in the
// VMM's mount namespace, which is gone once the VMM exits, so nothing here is
// still mounted — this is a plain recursive remove of an ordinary directory.
func (s Spec) Cleanup() error {
	if s.Dir == "" {
		return nil
	}
	if err := os.RemoveAll(string(s.Dir)); err != nil {
		return fmt.Errorf("jail: cleanup %s: %w", s.Dir, err)
	}
	return nil
}

func (s Spec) mounts() []mount {
	m := []mount{
		{source: s.Binary, target: GuestBinary[1:], dev: true, ro: true},
		{source: s.Kernel, target: GuestKernel[1:], dev: true, ro: true},
		{source: s.Rootfs, target: GuestRootfs[1:], dev: true},
		{source: "/dev/kvm", target: "dev/kvm", dev: true},
		{source: "/dev/urandom", target: "dev/urandom", dev: true},
		{source: "/dev/null", target: "dev/null", dev: true},
	}
	if s.Network {
		m = append(m, mount{source: "/dev/net/tun", target: "dev/net/tun", dev: true})
	}
	return m
}

// Enter runs inside the new namespaces, in the jail helper. It turns the empty
// jail directory into a working root and pivots into it, leaving the process
// with a filesystem that contains only what the VMM needs. It does not return
// on success — control passes to the VMM via exec in the caller.
func (s Spec) Enter() error {
	root := string(s.Dir)

	// pivot_root requires the new root to be a mount point, so bind the jail
	// directory onto itself. Marking it private stops any mount we make from
	// propagating back to the host — the isolation only works if it is one-way.
	if err := unix.Mount(root, root, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("jail: bind root: %w", err)
	}
	if err := unix.Mount("", root, "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("jail: make root private: %w", err)
	}

	for _, m := range s.mounts() {
		if err := bindInto(root, m); err != nil {
			return err
		}
	}

	// A fresh proc for the new pid namespace, so /proc reflects the jail rather
	// than the host. Mounted after the binds and before the pivot.
	if err := os.MkdirAll(filepath.Join(root, "proc"), 0o555); err != nil {
		return fmt.Errorf("jail: mkdir proc: %w", err)
	}
	if err := unix.Mount("proc", filepath.Join(root, "proc"), "proc", 0, ""); err != nil {
		return fmt.Errorf("jail: mount proc: %w", err)
	}

	return pivot(root)
}

// bindInto bind-mounts one host path to its place in the jail, creating the
// mount target first — a file for a device or binary, a directory otherwise.
func bindInto(root string, m mount) error {
	target := filepath.Join(root, m.target)

	if m.dev {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("jail: mkdir for %s: %w", m.target, err)
		}
		if f, err := os.OpenFile(target, os.O_CREATE, 0o600); err != nil {
			return fmt.Errorf("jail: create mount point %s: %w", m.target, err)
		} else {
			_ = f.Close()
		}
	} else if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("jail: mkdir %s: %w", m.target, err)
	}

	flags := uintptr(unix.MS_BIND)
	if err := unix.Mount(m.source, target, "", flags, ""); err != nil {
		return fmt.Errorf("jail: bind %s -> %s: %w", m.source, m.target, err)
	}
	// A read-only bind takes two steps: the bind, then a remount that adds the
	// flag. MS_RDONLY on the first mount is silently ignored for a bind.
	if m.ro {
		remount := uintptr(unix.MS_BIND | unix.MS_REMOUNT | unix.MS_RDONLY)
		if err := unix.Mount("", target, "", remount, ""); err != nil {
			return fmt.Errorf("jail: remount %s read-only: %w", m.target, err)
		}
	}
	return nil
}

// pivot makes root the process's root filesystem and detaches the old one, so
// the host filesystem is not merely hidden but genuinely unreachable.
func pivot(root string) error {
	oldRoot := filepath.Join(root, ".oldroot")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		return fmt.Errorf("jail: mkdir oldroot: %w", err)
	}
	if err := unix.PivotRoot(root, oldRoot); err != nil {
		return fmt.Errorf("jail: pivot_root: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("jail: chdir after pivot: %w", err)
	}
	// Detach the old root lazily and remove its now-empty mountpoint, so nothing
	// in the jail holds a path back to the host.
	if err := unix.Unmount("/.oldroot", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("jail: detach oldroot: %w", err)
	}
	if err := os.Remove("/.oldroot"); err != nil {
		return fmt.Errorf("jail: remove oldroot: %w", err)
	}
	return nil
}

// FirecrackerArgs are the arguments the jail helper exec's the VMM with, using
// the in-jail paths rather than the host ones.
func (s Spec) FirecrackerArgs() []string {
	return []string{"--api-sock", GuestAPISock(s.APISock)}
}

// GuestAPISock and GuestVsockSock are the in-jail paths, absolute from the
// jail's root, that the VMM is told to use. The daemon reaches the same sockets
// through the Host* paths.
func GuestAPISock(p string) string   { return "/" + strings.TrimPrefix(p, "/") }
func GuestVsockSock(p string) string { return "/" + strings.TrimPrefix(p, "/") }

// GuestVsock is the vsock path to hand Firecracker's SetVsock — inside the jail.
func (s Spec) GuestVsock() string { return GuestVsockSock(s.VsockSock) }

// HelperArgs are the flags the daemon passes eph-jail so it can rebuild this
// same Spec inside the namespaces.
func (s Spec) HelperArgs() []string {
	args := []string{
		"-dir", string(s.Dir),
		"-binary", s.Binary,
		"-kernel", s.Kernel,
		"-rootfs", s.Rootfs,
		"-api-sock", s.APISock,
		"-vsock-sock", s.VsockSock,
	}
	if s.Network {
		args = append(args, "-network")
	}
	return args
}
