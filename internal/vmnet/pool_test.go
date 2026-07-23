package vmnet

import (
	"net/netip"
	"testing"

	"golang.org/x/sys/unix"
)

func mustPool(t *testing.T, cidr string) *Pool {
	t.Helper()
	p, err := NewPool(cidr)
	if err != nil {
		t.Fatalf("NewPool(%q): %v", cidr, err)
	}
	return p
}

func mustTake(t *testing.T, p *Pool, id string) Lease {
	t.Helper()
	l, err := p.Take(id)
	if err != nil {
		t.Fatalf("Take(%q): %v", id, err)
	}
	return l
}

// The whole addressing scheme is arithmetic on the index, so pin the arithmetic.
func TestLeaseAddressesFollowTheIndex(t *testing.T) {
	p := mustPool(t, "10.79.0.0/16")

	want := []struct {
		host, guest string
	}{
		{"10.79.0.1", "10.79.0.2"},
		{"10.79.0.5", "10.79.0.6"},
		{"10.79.0.9", "10.79.0.10"},
	}
	for i, w := range want {
		l := mustTake(t, p, "0000000"+string(rune('a'+i)))
		if l.Index != i {
			t.Errorf("lease %d: index = %d, want %d", i, l.Index, i)
		}
		if got := l.Host.String(); got != w.host {
			t.Errorf("lease %d: host = %s, want %s", i, got, w.host)
		}
		if got := l.Guest.String(); got != w.guest {
			t.Errorf("lease %d: guest = %s, want %s", i, got, w.guest)
		}
	}
}

func TestNetmaskIsSlash30(t *testing.T) {
	if got := (Lease{}).Netmask().String(); got != "255.255.255.252" {
		t.Errorf("Netmask() = %s, want 255.255.255.252", got)
	}
	if got := (Lease{}).Bits(); got != 30 {
		t.Errorf("Bits() = %d, want 30", got)
	}
}

// The point of a link per machine is that no machine's address falls inside
// another machine's subnet — that is what makes reaching a neighbour a routing
// decision the host gets to refuse, rather than a local delivery it never sees.
func TestLinksDoNotOverlap(t *testing.T) {
	p := mustPool(t, "10.79.0.0/16")

	var leases []Lease
	for i := range 8 {
		leases = append(leases, mustTake(t, p, "aaaaaaa"+string(rune('0'+i))))
	}

	for i, a := range leases {
		subnet := netip.PrefixFrom(a.Host, a.Bits()).Masked()
		for j, b := range leases {
			if i == j {
				continue
			}
			if subnet.Contains(b.Guest) {
				t.Errorf("lease %d subnet %s contains lease %d's guest %s", i, subnet, j, b.Guest)
			}
			if subnet.Contains(b.Host) {
				t.Errorf("lease %d subnet %s contains lease %d's host %s", i, subnet, j, b.Host)
			}
		}
	}
}

func TestMACIsDerivedFromGuestAddress(t *testing.T) {
	p := mustPool(t, "10.79.0.0/16")
	l := mustTake(t, p, "deadbeef")

	// 10.79.0.2 -> 0a 4f 00 02, behind the locally-administered 06:00 prefix.
	if l.MAC != "06:00:0a:4f:00:02" {
		t.Errorf("MAC = %s, want 06:00:0a:4f:00:02", l.MAC)
	}
	// Locally administered, unicast: bit 1 set, bit 0 clear in the first octet.
	if first := byte(0x06); first&0x02 == 0 || first&0x01 != 0 {
		t.Error("first octet is not a locally administered unicast address")
	}
}

func TestReleasedLinkIsReusedBeforeAFreshOne(t *testing.T) {
	p := mustPool(t, "10.79.0.0/16")

	first := mustTake(t, p, "aaaaaaaa")
	second := mustTake(t, p, "bbbbbbbb")
	p.Put(first)

	third := mustTake(t, p, "cccccccc")
	if third.Index != first.Index {
		t.Errorf("third lease index = %d, want the released %d", third.Index, first.Index)
	}
	if third.Guest != first.Guest {
		t.Errorf("third lease guest = %s, want the released %s", third.Guest, first.Guest)
	}
	if second.Index == third.Index {
		t.Error("reused a link that was still held")
	}
}

func TestPutIsSafeForALeaseNeverTaken(t *testing.T) {
	p := mustPool(t, "10.79.0.0/16")
	p.Put(Lease{Index: 41})
	if p.InUse() != 0 {
		t.Errorf("InUse() = %d, want 0", p.InUse())
	}
}

func TestReservedLinkIsNotHandedOut(t *testing.T) {
	p := mustPool(t, "10.79.0.0/16")
	p.Reserve(0)
	p.Reserve(1)

	l := mustTake(t, p, "aaaaaaaa")
	if l.Index != 2 {
		t.Errorf("index = %d, want 2 (0 and 1 reserved)", l.Index)
	}
}

func TestPoolReportsWhenFull(t *testing.T) {
	// A /30 pool is exactly one link wide.
	p := mustPool(t, "10.79.0.0/30")
	if p.Size() != 1 {
		t.Fatalf("Size() = %d, want 1", p.Size())
	}
	mustTake(t, p, "aaaaaaaa")

	if _, err := p.Take("bbbbbbbb"); err == nil {
		t.Fatal("Take on a full pool returned no error")
	}
}

func TestPoolRejectsUnusableRanges(t *testing.T) {
	for _, cidr := range []string{
		"fd00::/64",      // not IPv4
		"10.79.0.0/31",   // narrower than one link
		"10.79.0.0",      // not a prefix
		"not-an-address", // not anything
	} {
		if _, err := NewPool(cidr); err == nil {
			t.Errorf("NewPool(%q) accepted an unusable range", cidr)
		}
	}
}

// A pool written with a host address in it means the range it falls in; without
// masking, every address handed out would be shifted off the boundary.
func TestPoolMasksTheGivenAddress(t *testing.T) {
	p := mustPool(t, "10.79.3.77/16")
	l := mustTake(t, p, "aaaaaaaa")
	if got := l.Host.String(); got != "10.79.0.1" {
		t.Errorf("host = %s, want 10.79.0.1", got)
	}
}

func TestTakeRequiresAMachineID(t *testing.T) {
	p := mustPool(t, "10.79.0.0/16")
	if _, err := p.Take(""); err == nil {
		t.Fatal("Take(\"\") returned no error")
	}
	if p.InUse() != 0 {
		t.Errorf("a rejected Take consumed a link: InUse() = %d", p.InUse())
	}
}

// Machine ids are 8 hex characters, so the interface name is 11 bytes and fits
// with room to spare. This is a guard for whenever ids get longer: the kernel
// truncates nothing, it just refuses.
func TestTapNameFitsTheKernelLimit(t *testing.T) {
	name := TapName("0f8bdebe")
	if name != "eph0f8bdebe" {
		t.Errorf("TapName = %s, want eph0f8bdebe", name)
	}
	if err := validTapName(name); err != nil {
		t.Errorf("validTapName(%q): %v", name, err)
	}

	tooLong := TapName("0123456789abcdef")
	if err := validTapName(tooLong); err == nil {
		t.Errorf("validTapName(%q) accepted %d bytes, limit is %d",
			tooLong, len(tooLong), unix.IFNAMSIZ-1)
	}
}
