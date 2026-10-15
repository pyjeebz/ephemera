package hub

import (
	"errors"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// TestAcceptEnforcesLimits: announces over the relay's TTL or size
// limit, or with no TTL, are refused with a reason and never
// registered; one at the limits is accepted.
func TestAcceptEnforcesLimits(t *testing.T) {
	reg := share.NewRegistry(time.Hour)
	b := &Bridge{Registry: reg, Limits: Limits{MaxTTLSec: 600, MaxSize: 1 << 20}}

	for name, tc := range map[string]struct{ ttl, size int64 }{
		"ttl over":  {601, 1024},
		"size over": {60, 1<<20 + 1},
		"no ttl":    {0, 1024},
	} {
		key, id := protocol.NewOwnerKey()
		conn, daemonR := announceOnly(t, protocol.Send{
			ShareID: id, OwnerKey: key, Name: "v.mp4", Size: tc.size, MIME: "video/mp4", TTLSec: tc.ttl,
		})
		if _, err := b.Accept(conn); !errors.Is(err, ErrOverLimit) {
			t.Fatalf("%s: accept err = %v, want ErrOverLimit", name, err)
		}
		expectRefusal(t, daemonR, "denied")
		if _, _, err := reg.Lookup(id); !errors.Is(err, share.ErrNotFound) {
			t.Fatalf("%s: refused share registered: %v", name, err)
		}
	}

	key, id := protocol.NewOwnerKey()
	conn, _ := announceOnly(t, protocol.Send{
		ShareID: id, OwnerKey: key, Name: "v.mp4", Size: 1 << 20, MIME: "video/mp4", TTLSec: 600,
	})
	d, err := b.Accept(conn)
	if err != nil {
		t.Fatalf("announce at the limits: %v", err)
	}
	d.Close()
}

// TestZeroLimitsAreDefaults: a zero Bridge is capped at the defaults,
// never unlimited.
func TestZeroLimitsAreDefaults(t *testing.T) {
	b := &Bridge{Registry: share.NewRegistry(time.Hour)}
	key, id := protocol.NewOwnerKey()
	conn, daemonR := announceOnly(t, protocol.Send{
		ShareID: id, OwnerKey: key, Name: "v.mp4", Size: 1024, MIME: "video/mp4", TTLSec: DefaultMaxTTLSec + 1,
	})
	if _, err := b.Accept(conn); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("accept err = %v, want ErrOverLimit", err)
	}
	expectRefusal(t, daemonR, "denied")
}
