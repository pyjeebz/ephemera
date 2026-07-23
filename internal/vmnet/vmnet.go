package vmnet

import (
	"fmt"
	"net"
	"net/netip"
)

// Manager owns the host side of machine networking: which links are spoken for,
// and the interfaces that back them.
//
// It deliberately does not know about firewalls. Handing a machine a working
// link and deciding where that link is allowed to reach are separate jobs, and
// keeping them separate is what lets the second one default to "nowhere".
type Manager struct {
	pool   *Pool
	helper string // eph-netadmin, the one thing that holds CAP_NET_ADMIN
}

// New prepares a manager over cidr (or DefaultPool when empty) that builds
// interfaces by executing helper — eph-netadmin, resolved by HelperPath when
// the path is left empty.
func New(cidr, helper string) (*Manager, error) {
	if cidr == "" {
		cidr = DefaultPool
	}
	p, err := NewPool(cidr)
	if err != nil {
		return nil, err
	}
	return &Manager{pool: p, helper: helper}, nil
}

// Pool exposes the allocator, for callers that need to reserve links they
// already know about.
func (m *Manager) Pool() *Pool { return m.pool }

// Attach builds a machine's link and returns it ready for a VMM to open.
//
// The interface exists and is up before the VMM starts. That ordering is not
// optional: Firecracker opens the TAP by name when the network interface is
// configured, and a name that is not there yet is simply an error.
func (m *Manager) Attach(id string) (Lease, error) {
	for {
		l, err := m.pool.Take(id)
		if err != nil {
			return Lease{}, err
		}

		// A manager only knows about the links it handed out itself, and it is
		// not the only thing on the host: a second ephemerad, a standalone eph
		// run, or an interface left behind by a crash can all be holding an
		// address this pool believes is free. Two interfaces in the same subnet
		// do not fail loudly — they make the routing table ambiguous and the
		// symptom shows up later as a machine whose replies go to the wrong
		// place. So ask the kernel before claiming it.
		//
		// The index stays marked as used on the way round: something is on that
		// address, and this manager is not the thing that will free it.
		taken, err := addressInUse(l.Host)
		if err != nil {
			m.pool.Put(l)
			return Lease{}, err
		}
		if taken {
			continue
		}

		// One helper call creates and configures the interface. If it fails the
		// helper leaves nothing behind, but ask it to remove the name anyway —
		// idempotent, and a half-built link that lingers holds the address.
		if err := createTapVia(m.helper, l.Tap, l.Host, l.Netmask()); err != nil {
			_ = removeTapVia(m.helper, l.Tap)
			m.pool.Put(l)
			return Lease{}, err
		}
		return l, nil
	}
}

// addressInUse reports whether any interface on the host already answers to an
// address. Reading the interface list needs no privilege — it is the same
// question `ip addr` asks.
func addressInUse(a netip.Addr) (bool, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false, fmt.Errorf("vmnet: list host addresses: %w", err)
	}
	for _, ad := range addrs {
		n, ok := ad.(*net.IPNet)
		if !ok {
			continue
		}
		if got, ok := netip.AddrFromSlice(n.IP); ok && got.Unmap() == a {
			return true, nil
		}
	}
	return false, nil
}

// Detach tears down a machine's link and frees it for reuse.
//
// The lease goes back to the pool whether or not the interface came down
// cleanly: a name we cannot delete is a problem, but refusing to reuse the
// address behind it turns one stuck interface into a slow leak of the pool.
func (m *Manager) Detach(l Lease) error {
	err := removeTapVia(m.helper, l.Tap)
	m.pool.Put(l)
	return err
}

// DestroyTap removes an interface by name through the helper, for cleaning up
// after a daemon that is no longer around to hold the lease that made it.
func DestroyTap(helper, name string) error { return removeTapVia(helper, name) }
