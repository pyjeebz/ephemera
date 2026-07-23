package vmnet

import (
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/sys/unix"
)

// tunDevice is the clone device: opening it and naming an interface is how a
// TAP gets created. There is no /sys knob and no netlink message involved in
// the creation itself — it is one ioctl on one character device.
const tunDevice = "/dev/net/tun"

// createTap makes a persistent TAP device the VMM can open by name.
//
// Two flags decide what kind of device comes out:
//
//	IFF_TAP    an ethernet device carrying frames. The IFF_TUN alternative
//	           carries bare IP packets, which virtio-net would not know what to
//	           do with — the guest expects to be holding a network card.
//	IFF_NO_PI  no packet-information header. Without it the kernel prepends four
//	           bytes of its own to every frame and the VMM sees garbage.
func createTap(name string) error {
	if err := validTapName(name); err != nil {
		return err
	}

	fd, err := unix.Open(tunDevice, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("vmnet: open %s: %w", tunDevice, err)
	}
	defer unix.Close(fd)

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("vmnet: interface name %q: %w", name, err)
	}
	ifr.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		return fmt.Errorf("vmnet: create tap %s: %w", name, err)
	}

	// A TAP's default lifetime is the lifetime of the file descriptor that made
	// it, and we are about to close ours: Firecracker opens the device by name
	// itself, so there is no reason for the daemon to hold an fd per machine
	// open forever. Persisting it hands ownership to the kernel until somebody
	// takes it back, which destroyTap does.
	if err := unix.IoctlSetInt(fd, unix.TUNSETPERSIST, 1); err != nil {
		return fmt.Errorf("vmnet: persist tap %s: %w", name, err)
	}
	return nil
}

// destroyTap removes a TAP device.
//
// There is no "delete" ioctl — persistence is the only thing keeping the device
// alive, so clearing it and closing the descriptor is the delete. If the VMM
// still has the device open, the kernel waits for that descriptor too, which is
// exactly the ordering we want: the interface outlives the machine by precisely
// as long as the machine needs it.
//
// Calling this for a device that no longer exists briefly recreates it, because
// TUNSETIFF creates on demand. That makes the function idempotent by accident
// rather than by design, but idempotent is what teardown paths need.
func destroyTap(name string) error {
	if err := validTapName(name); err != nil {
		return err
	}

	fd, err := unix.Open(tunDevice, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("vmnet: open %s: %w", tunDevice, err)
	}
	defer unix.Close(fd)

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("vmnet: interface name %q: %w", name, err)
	}
	ifr.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		if errors.Is(err, unix.ENODEV) {
			return nil
		}
		return fmt.Errorf("vmnet: attach tap %s: %w", name, err)
	}
	if err := unix.IoctlSetInt(fd, unix.TUNSETPERSIST, 0); err != nil {
		return fmt.Errorf("vmnet: release tap %s: %w", name, err)
	}
	return nil
}

// configureTap gives the host end of the link an address and brings it up.
//
// These are the old ioctl interfaces — SIOCSIFADDR and friends, the ones
// ifconfig used before ip existed. rtnetlink is the modern way and the only way
// to reach anything IPv6 or anything with more than one address per interface,
// but it means hand-encoding netlink messages for what is here three ioctls on
// a datagram socket. The socket is a formality: nothing is sent on it, it is
// just the handle the kernel wants these requests to arrive through.
func configureTap(name string, addr, mask netip.Addr) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("vmnet: open control socket: %w", err)
	}
	defer unix.Close(fd)

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("vmnet: interface name %q: %w", name, err)
	}

	for _, step := range []struct {
		what string
		req  uint
		addr netip.Addr
	}{
		{"address", unix.SIOCSIFADDR, addr},
		{"netmask", unix.SIOCSIFNETMASK, mask},
	} {
		if err := ifr.SetInet4Addr(step.addr.AsSlice()); err != nil {
			return fmt.Errorf("vmnet: encode %s %s: %w", step.what, step.addr, err)
		}
		if err := unix.IoctlIfreq(fd, step.req, ifr); err != nil {
			return fmt.Errorf("vmnet: set %s on %s: %w", step.what, name, err)
		}
	}

	// Read the flags before setting them: an interface arrives with flags we do
	// not know about and have no business clearing.
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("vmnet: read flags on %s: %w", name, err)
	}
	flags := ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING
	ifr.SetUint16(flags)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("vmnet: bring up %s: %w", name, err)
	}
	return nil
}

// ErrNoNetAdmin says the process cannot manage interfaces.
var ErrNoNetAdmin = errors.New("vmnet: CAP_NET_ADMIN is required to create TAP devices")

// Available reports whether this process can build machine networks, and is
// meant to be called at startup so the answer arrives before the first machine
// rather than during it.
//
// The check is a capability query rather than a trial run: making a throwaway
// TAP would work, but a daemon that probes by mutating the host's network is a
// daemon nobody should run.
func Available() error {
	ok, err := hasNetAdmin()
	if err != nil {
		return fmt.Errorf("vmnet: read own capabilities: %w", err)
	}
	if !ok {
		return ErrNoNetAdmin
	}
	if fd, err := unix.Open(tunDevice, unix.O_RDWR|unix.O_CLOEXEC, 0); err != nil {
		return fmt.Errorf("vmnet: %s not usable: %w", tunDevice, err)
	} else {
		unix.Close(fd)
	}
	return nil
}

// hasNetAdmin asks the kernel for this process's own capability set.
//
// capget is the direct answer; the alternative is parsing CapEff out of
// /proc/self/status, which is the same bitmask rendered as hex for humans.
func hasNetAdmin() (bool, error) {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	// Version 3 capability sets are 64 bits wide, delivered as two 32-bit
	// words. CAP_NET_ADMIN is 12, so it lives in the first one.
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return false, err
	}
	return data[0].Effective&(1<<unix.CAP_NET_ADMIN) != 0, nil
}

func validTapName(name string) error {
	switch {
	case name == "":
		return errors.New("vmnet: interface name is empty")
	case len(name) >= unix.IFNAMSIZ:
		return fmt.Errorf("vmnet: interface name %q is %d bytes, limit is %d",
			name, len(name), unix.IFNAMSIZ-1)
	}
	return nil
}
