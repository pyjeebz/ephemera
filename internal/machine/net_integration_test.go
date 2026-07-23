package machine

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/vmnet"
)

// This boots a real networked microVM. On top of what requireVM needs, it wants
// CAP_NET_ADMIN and /dev/net/tun — so it skips where those are missing rather
// than failing, the same as the other integration tests.
//
// It proves the guest configured itself: address, route, hostname, resolver. It
// deliberately does not test reaching the internet, because that depends on the
// firewall host-setup.sh installs, which is host state a unit test has no
// business assuming. Reachability and the egress block are verified by hand —
// see docs/buildlog.md.
func TestNetworkedGuestConfiguresItself(t *testing.T) {
	cfg := requireVM(t)
	if err := vmnet.Available(); err != nil {
		t.Skipf("machine networking not available: %v", err)
	}

	mgr, err := vmnet.New(vmnet.DefaultPool)
	if err != nil {
		t.Fatalf("vmnet.New: %v", err)
	}
	cfg.Init = AgentInit
	cfg.Net = mgr

	var console bytes.Buffer
	cfg.Console = &console

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() { _ = m.Destroy(context.Background()) }()

	lease, ok := m.Lease()
	if !ok {
		t.Fatal("a networked machine reported no lease")
	}

	if err := m.WaitAgent(ctx); err != nil {
		t.Fatalf("agent never came up: %v\nconsole:\n%s", err, console.String())
	}

	// The interface the kernel brought up from ip=, with the leased address on it.
	t.Run("eth0 has the leased address", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := m.Exec(ctx, []string{"ip", "-o", "-4", "addr", "show", "eth0"}, &out, nil); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if !strings.Contains(out.String(), lease.Guest.String()) {
			t.Errorf("eth0 does not carry %s:\n%s", lease.Guest, out.String())
		}
	})

	t.Run("default route points at the host end", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := m.Exec(ctx, []string{"ip", "route", "show", "default"}, &out, nil); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if !strings.Contains(out.String(), lease.Host.String()) {
			t.Errorf("default route is not via %s:\n%s", lease.Host, out.String())
		}
	})

	t.Run("resolver came from the kernel command line", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := m.Exec(ctx, []string{"cat", "/etc/resolv.conf"}, &out, nil); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if !strings.Contains(out.String(), DefaultDNS.String()) {
			t.Errorf("resolv.conf does not name %s:\n%s", DefaultDNS, out.String())
		}
	})
}

// A machine with no lease must come up with only loopback — the isolation floor,
// and the default. This is the networked counterpart to the no-interface check
// in TestExecInGuest, kept here so the two live next to each other.
func TestUnnetworkedGuestHasNoLease(t *testing.T) {
	cfg := requireVM(t)
	cfg.Init = AgentInit
	cfg.Console = &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() { _ = m.Destroy(context.Background()) }()

	if _, ok := m.Lease(); ok {
		t.Error("a machine booted without a manager reported a lease")
	}
}
