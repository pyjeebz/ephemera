package hub

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
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

type suspendedSet map[string]bool

func (s suspendedSet) Suspended(account string) bool { return s[account] }

// TestAcceptRefusesSuspendedAccount: a valid token for a suspended
// account is refused with a reason; other accounts still share.
func TestAcceptRefusesSuspendedAccount(t *testing.T) {
	reg := share.NewRegistry(time.Hour)
	b := &Bridge{
		Registry:    reg,
		Auth:        &fakeAuth{accounts: map[string]Account{"bad": {Name: "mallory"}, "good": {Name: "alice"}}},
		Suspensions: suspendedSet{"mallory": true},
	}

	conn, daemonR := announceWith(t, protocol.Hello{Version: "0.1", Token: "bad"}, newAnnounce(60, 1024))
	if _, err := b.Accept(conn); !errors.Is(err, ErrSuspended) {
		t.Fatalf("suspended account: %v, want ErrSuspended", err)
	}
	if e := expectRefusal(t, daemonR, "denied"); e.Message != "this sender account is suspended" {
		t.Errorf("reason = %q", e.Message)
	}

	conn, _ = announceWith(t, protocol.Hello{Version: "0.1", Token: "good"}, newAnnounce(60, 1024))
	d, err := b.Accept(conn)
	if err != nil {
		t.Fatalf("other account refused: %v", err)
	}
	d.Close()
}

// TestAcceptWelcomesWithLink: an accepted daemon gets its link under
// the relay's public address; without one, the welcome has no link.
func TestAcceptWelcomesWithLink(t *testing.T) {
	b := &Bridge{Registry: share.NewRegistry(time.Hour)}
	ann := newAnnounce(60, 1024)
	conn, daemonR := announceWith(t, protocol.Hello{Version: "0.1"}, ann)
	d, err := b.AcceptAt(conn, "https://relay.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fr, err := protocol.ReadFrame(daemonR)
	if err != nil {
		t.Fatal(err)
	}
	var w protocol.Welcome
	if fr.Type != protocol.TWelcome || json.Unmarshal(fr.JSON, &w) != nil {
		t.Fatalf("first frame type %d, want TWelcome", fr.Type)
	}
	if want := "https://relay.example.com/" + ann.ShareID; w.Link != want || w.ShareID != ann.ShareID {
		t.Fatalf("welcome = %+v, want link %s", w, want)
	}

	// no public address: still welcomed (the daemon learns its resume
	// point there), with no link to print
	conn, daemonR = announceWith(t, protocol.Hello{Version: "0.1"}, newAnnounce(60, 1024))
	d2, err := b.Accept(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	fr, err = protocol.ReadFrame(daemonR)
	w = protocol.Welcome{}
	if err != nil || fr.Type != protocol.TWelcome || json.Unmarshal(fr.JSON, &w) != nil || w.Link != "" {
		t.Fatalf("without a public URL: first frame %+v (%v), want a welcome with no link", fr, err)
	}
}

// countStarts allows the first left calls, then refuses.
type countStarts struct{ left int }

func (c *countStarts) Allow(string) (bool, int, time.Duration) {
	c.left--
	return c.left >= 0, max(c.left, 0), 30 * time.Minute
}

// TestAcceptGuests: with a guest policy, a sender without a token
// shares under the guest limits, as an account named for its
// address; a wrong token is still refused, and so is a guest the
// relay can't place. New shares count against the address; redials
// don't.
func TestAcceptGuests(t *testing.T) {
	reg := share.NewRegistry(time.Hour)
	starts := &countStarts{left: 1}
	b := &Bridge{
		Registry: reg,
		Auth:     &fakeAuth{accounts: map[string]Account{"good": {Name: "alice"}}},
		Guests:   &Guests{Limits: Limits{MaxTTLSec: 3600, MaxSize: 1 << 20}, Starts: starts},
	}
	guest := protocol.Hello{Version: "0.1"}

	ann := newAnnounce(3600, 1024)
	conn, _ := announceWith(t, guest, ann)
	d, err := b.AcceptFrom(conn, "", "203.0.113.7")
	if err != nil {
		t.Fatalf("guest within limits: %v", err)
	}
	defer d.Close()
	if info, _, err := reg.Lookup(ann.ShareID); err != nil || info.Account != "guest-203.0.113.7" {
		t.Fatalf("guest share account = %q (err %v)", info.Account, err)
	}

	// a redial of the same share isn't a new start
	conn, _ = announceWith(t, guest, ann)
	d2, err := b.AcceptFrom(conn, "", "203.0.113.7")
	if err != nil {
		t.Fatalf("guest redial: %v", err)
	}
	defer d2.Close()

	// a second new share is over the address's allowance
	conn, daemonR := announceWith(t, guest, newAnnounce(60, 1024))
	if _, err := b.AcceptFrom(conn, "", "203.0.113.7"); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("second guest start: %v, want ErrOverLimit", err)
	}
	if e := expectRefusal(t, daemonR, "denied"); !strings.Contains(e.Message, "30m0s") || !strings.Contains(e.Message, "sign in") {
		t.Errorf("start refusal = %q", e.Message)
	}

	// over the guest limits: refused, with what signing in gets
	conn, daemonR = announceWith(t, guest, newAnnounce(7200, 1024))
	if _, err := b.AcceptFrom(conn, "", "198.51.100.1"); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("guest over ttl: %v, want ErrOverLimit", err)
	}
	if e := expectRefusal(t, daemonR, "denied"); !strings.Contains(e.Message, "sign in") {
		t.Errorf("limit refusal = %q", e.Message)
	}

	// no address to name the guest by, or a wrong token: refused
	conn, daemonR = announceWith(t, guest, newAnnounce(60, 1024))
	if _, err := b.Accept(conn); !errors.Is(err, ErrNoToken) {
		t.Fatalf("guest without a peer: %v, want ErrNoToken", err)
	}
	expectRefusal(t, daemonR, "denied")
	conn, daemonR = announceWith(t, protocol.Hello{Version: "0.1", Token: "bad"}, newAnnounce(60, 1024))
	if _, err := b.AcceptFrom(conn, "", "198.51.100.1"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("wrong token with guests on: %v, want ErrBadToken", err)
	}
	expectRefusal(t, daemonR, "denied")
}
