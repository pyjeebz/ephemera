// Command eph-jail is the second half of the jail: the part that runs inside
// the namespaces.
//
// The daemon spawns it with the clone flags and id mappings from jail.Spec, so
// by the time main runs it is already root inside a fresh user, mount, and pid
// namespace. Its whole job is to turn the prepared jail directory into a root
// filesystem, pivot into it, and hand control to the VMM by exec. It is not a
// long-running process — after a successful exec it has ceased to be eph-jail at
// all and is the firecracker binary instead.
//
// It is a separate program because Go gives no hook between clone and exec in
// which to run setup: the namespaces have to be entered by spawning something,
// and that something is this.
package main

import (
	"flag"
	"fmt"
	"os"
	"syscall"

	"github.com/pyjeebz/ephemera/internal/jail"
)

func main() {
	var s jail.Spec
	var dir string
	flag.StringVar(&dir, "dir", "", "host jail directory (also the root after pivot)")
	flag.StringVar(&s.Binary, "binary", "", "firecracker binary on the host")
	flag.StringVar(&s.Kernel, "kernel", "", "guest kernel on the host")
	flag.StringVar(&s.Rootfs, "rootfs", "", "guest rootfs on the host")
	flag.StringVar(&s.APISock, "api-sock", "run/firecracker.sock", "API socket path inside the jail")
	flag.StringVar(&s.VsockSock, "vsock-sock", "run/vsock.sock", "vsock socket path inside the jail")
	flag.StringVar(&s.SnapState, "snap-state", "", "snapshot state file to bind read-only (restore)")
	flag.StringVar(&s.SnapMem, "snap-mem", "", "snapshot memory file to bind read-only (restore)")
	flag.BoolVar(&s.Network, "network", false, "bind /dev/net/tun for a networked machine")
	flag.Parse()
	s.Dir = jail.Dir(dir)

	if err := run(s); err != nil {
		fmt.Fprintln(os.Stderr, "eph-jail:", err)
		os.Exit(1)
	}
}

func run(s jail.Spec) error {
	// Build the chroot and move into it. After this the host filesystem is gone.
	if err := s.Enter(); err != nil {
		return err
	}

	// Become the VMM. A successful Exec never returns; on failure it is the only
	// place an error can still be reported, since the console is all that is left.
	argv := append([]string{jail.GuestBinary}, s.FirecrackerArgs()...)
	env := []string{
		"PATH=/",
		"HOME=/",
	}
	if err := syscall.Exec(jail.GuestBinary, argv, env); err != nil {
		return fmt.Errorf("exec firecracker: %w", err)
	}
	return nil // unreachable
}
