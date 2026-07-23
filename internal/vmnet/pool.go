// Package vmnet gives a machine a network without giving it the network.
//
// Every machine gets a point-to-point link of its own: a TAP device on the host
// carrying a /30 out of a private pool, one address for the host end and one for
// the guest. There is no bridge and no shared segment, so two machines have no
// path to each other that does not pass through the host's routing table — where
// the firewall gets to refuse it. Isolation comes out of the topology rather
// than out of a rule someone has to remember to write.
package vmnet

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"sync"
)

// DefaultPool is the range machines are numbered out of. RFC 1918 space, and
// deliberately an odd corner of it: the point is to not collide with whatever
// the host is already doing with 10.0.0.0/8 or 172.17.0.0/16.
const DefaultPool = "10.79.0.0/16"

// linkBits is the prefix length of one machine's link. A /30 spends four
// addresses to carry two — network, host, guest, broadcast — which is wasteful
// in a way that stopped mattering the moment we picked a /16 to spend it from.
// RFC 3021 /31s would halve it, at the cost of both ends having to agree there
// is no broadcast address.
const linkBits = 30

// addrsPerLink is how far apart two consecutive links are.
const addrsPerLink = 1 << (32 - linkBits)

// Lease is one machine's link: the host interface and the two addresses on it.
//
// Host is the guest's default gateway. Nothing else in the pool is reachable
// from the guest — the next machine's addresses are a different link entirely,
// and getting there means being routed by the host.
type Lease struct {
	Index int
	Tap   string     // host interface name, e.g. eph0f8bdebe
	Host  netip.Addr // .1 — the guest's default gateway
	Guest netip.Addr // .2
	MAC   string     // the guest's, derived from Guest
}

// Bits is the prefix length of the link, for callers writing a netmask.
func (Lease) Bits() int { return linkBits }

// Netmask renders the link's prefix in the dotted-quad form the guest kernel's
// ip= parameter insists on. It has no interest in CIDR.
//
// Complementing "one link's worth of addresses, minus one" is the same value as
// shifting a full mask left by the host bits, without the constant overflow.
func (Lease) Netmask() netip.Addr {
	return addrFrom32(^uint32(addrsPerLink - 1))
}

// Pool hands out links, one per machine.
//
// Allocation is by index rather than by address: index 0 is the first /30 in
// the range, index 1 the second, and the addresses fall out of arithmetic. That
// keeps a machine's whole network identity — interface name, both addresses,
// MAC — derivable from one small integer.
type Pool struct {
	prefix netip.Prefix
	size   int

	mu   sync.Mutex
	used map[int]bool
}

// NewPool prepares a pool over cidr, which must be an IPv4 range with room for
// at least one link.
func NewPool(cidr string) (*Pool, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("vmnet: parse pool %q: %w", cidr, err)
	}
	if !p.Addr().Is4() {
		return nil, fmt.Errorf("vmnet: pool %s is not IPv4", cidr)
	}
	if p.Bits() > linkBits {
		return nil, fmt.Errorf("vmnet: pool %s is smaller than one /%d link", cidr, linkBits)
	}
	// Normalise: a pool written as 10.79.0.5/16 means the same range as
	// 10.79.0.0/16, and the arithmetic below assumes we are on the boundary.
	p = p.Masked()

	return &Pool{
		prefix: p,
		size:   1 << (linkBits - p.Bits()),
		used:   make(map[int]bool),
	}, nil
}

// Size is how many machines can hold a lease at once.
func (p *Pool) Size() int { return p.size }

// Take reserves the lowest free link for a machine.
//
// Lowest-free rather than next-in-sequence is deliberate: addresses get reused
// promptly, so a long-running daemon keeps handing out eph0..eph3 instead of
// wandering off into five-digit indices that are miserable to read in tcpdump.
func (p *Pool) Take(id string) (Lease, error) {
	if id == "" {
		return Lease{}, fmt.Errorf("vmnet: machine id is required")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.size {
		if p.used[i] {
			continue
		}
		l, err := p.leaseAt(i, id)
		if err != nil {
			return Lease{}, err
		}
		p.used[i] = true
		return l, nil
	}
	return Lease{}, fmt.Errorf("vmnet: pool %s is full (%d links)", p.prefix, p.size)
}

// Put returns a lease to the pool. Releasing a lease that was never taken is
// not an error: teardown paths run more than once and should stay quiet.
func (p *Pool) Put(l Lease) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.used, l.Index)
}

// Reserve marks a link as in use without allocating one, so a daemon that has
// just read its records back off disk does not hand the same address to a new
// machine.
func (p *Pool) Reserve(index int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index >= 0 && index < p.size {
		p.used[index] = true
	}
}

// InUse reports how many links are currently held.
func (p *Pool) InUse() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.used)
}

func (p *Pool) leaseAt(index int, id string) (Lease, error) {
	tap := TapName(id)
	if err := validTapName(tap); err != nil {
		return Lease{}, err
	}

	base := addr32(p.prefix.Addr()) + uint32(index)*addrsPerLink
	guest := addrFrom32(base + 2)

	return Lease{
		Index: index,
		Tap:   tap,
		Host:  addrFrom32(base + 1),
		Guest: guest,
		MAC:   macFor(guest),
	}, nil
}

// TapName is the host interface name for a machine.
func TapName(id string) string { return "eph" + id }

// macFor derives a machine's MAC from its address.
//
// 06 as the first octet sets the locally-administered bit and clears the
// multicast one, which is the range reserved for exactly this — addresses
// nobody bought. Deriving the rest from the IPv4 address means the MAC is
// stable across reboots, unique by construction, and readable straight off a
// packet capture.
func macFor(a netip.Addr) string {
	b := a.As4()
	return fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3])
}

func addr32(a netip.Addr) uint32 {
	b := a.As4()
	return binary.BigEndian.Uint32(b[:])
}

func addrFrom32(v uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return netip.AddrFrom4(b)
}
