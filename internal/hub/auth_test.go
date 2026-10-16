package hub

import (
	"errors"
	"io"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// fakeAuth accepts tokens it knows; down simulates an accounts
// service outage.
type fakeAuth struct {
	accounts map[string]Account
	down     bool
}

func (f *fakeAuth) Authenticate(token string) (Account, error) {
	switch {
	case f.down:
		return Account{}, errors.New("accounts service unreachable")
	case token == "":
		return Account{}, ErrNoToken
	}
	acct, ok := f.accounts[token]
	if !ok {
		return Account{}, ErrBadToken
	}
	return acct, nil
}

func newAnnounce(ttl, size int64) protocol.Send {
	key, id := protocol.NewOwnerKey()
	return protocol.Send{ShareID: id, OwnerKey: key, Name: "v.mp4", Size: size, MIME: "video/mp4", TTLSec: ttl}
}

// TestAcceptAuthRefusals: no token and a wrong token are final
// refusals with a reason; nothing is registered.
func TestAcceptAuthRefusals(t *testing.T) {
	reg := share.NewRegistry(time.Hour)
	b := &Bridge{Registry: reg, Auth: &fakeAuth{accounts: map[string]Account{"good": {Name: "alice"}}}}

	for name, tc := range map[string]struct {
		token string
		want  error
	}{
		"no token":    {"", ErrNoToken},
		"wrong token": {"bad", ErrBadToken},
	} {
		ann := newAnnounce(60, 1024)
		conn, daemonR := announceWith(t, protocol.Hello{Version: "0.1", Token: tc.token}, ann)
		if _, err := b.Accept(conn); !errors.Is(err, tc.want) {
			t.Fatalf("%s: accept err = %v, want %v", name, err, tc.want)
		}
		expectRefusal(t, daemonR, "denied")
		if _, _, err := reg.Lookup(ann.ShareID); !errors.Is(err, share.ErrNotFound) {
			t.Fatalf("%s: refused share registered: %v", name, err)
		}
	}
}

// TestAcceptAuthOutageIsNotARefusal: when the accounts service is
// down the relay hangs up without TErr, so the daemon redials
// instead of giving up on a share that may be fine.
func TestAcceptAuthOutageIsNotARefusal(t *testing.T) {
	b := &Bridge{Registry: share.NewRegistry(time.Hour), Auth: &fakeAuth{down: true}}
	conn, daemonR := announceWith(t, protocol.Hello{Version: "0.1", Token: "good"}, newAnnounce(60, 1024))
	if _, err := b.Accept(conn); err == nil || errors.Is(err, ErrNoToken) || errors.Is(err, ErrBadToken) {
		t.Fatalf("accept err = %v, want a transient error", err)
	}
	if fr, err := protocol.ReadFrame(daemonR); !errors.Is(err, io.EOF) {
		t.Fatalf("daemon got frame %+v (err %v), want a bare hangup", fr, err)
	}
}

// TestAcceptAuthAccountAndLimits: an accepted sender's account is
// recorded on the share, and its own limits replace the relay's.
func TestAcceptAuthAccountAndLimits(t *testing.T) {
	reg := share.NewRegistry(time.Hour)
	b := &Bridge{
		Registry: reg,
		Limits:   Limits{MaxTTLSec: 600, MaxSize: 1 << 20},
		Auth: &fakeAuth{accounts: map[string]Account{
			"free": {Name: "freebie"},
			"paid": {Name: "pro", Limits: Limits{MaxTTLSec: 7200}},
		}},
	}

	// relay limits for an account without its own
	conn, daemonR := announceWith(t, protocol.Hello{Version: "0.1", Token: "free"}, newAnnounce(3600, 1024))
	if _, err := b.Accept(conn); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("free account over relay ttl: %v, want ErrOverLimit", err)
	}
	expectRefusal(t, daemonR, "denied")

	// the paid account's ttl replaces the relay's; its size falls back
	ann := newAnnounce(3600, 1024)
	conn, _ = announceWith(t, protocol.Hello{Version: "0.1", Token: "paid"}, ann)
	d, err := b.Accept(conn)
	if err != nil {
		t.Fatalf("paid account within its ttl: %v", err)
	}
	defer d.Close()
	info, _, err := reg.Lookup(ann.ShareID)
	if err != nil || info.Account != "pro" {
		t.Fatalf("share account = %q (err %v), want pro", info.Account, err)
	}

	conn, daemonR = announceWith(t, protocol.Hello{Version: "0.1", Token: "paid"}, newAnnounce(60, 1<<20+1))
	if _, err := b.Accept(conn); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("paid account over relay size: %v, want ErrOverLimit", err)
	}
	expectRefusal(t, daemonR, "denied")
}
