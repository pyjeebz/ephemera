package share

import (
	"errors"
	"testing"
	"time"
)

// TestKillTombstone: a killed id is refused until its original
// expiry, then the tombstone is swept and the id is free again.
func TestKillTombstone(t *testing.T) {
	r := NewRegistry(time.Hour)
	defer r.Stop()

	expires := time.Now().Add(80 * time.Millisecond)
	if err := r.Register(Info{ID: "k", Expires: expires}, &fakeHandle{}); err != nil {
		t.Fatal(err)
	}
	if !r.Kill("k") {
		t.Fatal("kill returned false")
	}
	if err := r.Register(Info{ID: "k", Expires: time.Now().Add(time.Hour)}, &fakeHandle{}); !errors.Is(err, ErrKilled) {
		t.Fatalf("register after kill: %v, want ErrKilled", err)
	}
	if _, _, err := r.Lookup("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup after kill: %v, want ErrNotFound", err)
	}

	time.Sleep(100 * time.Millisecond)
	r.Sweep()
	r.mu.RLock()
	_, still := r.killed["k"]
	r.mu.RUnlock()
	if still {
		t.Fatal("tombstone not swept after the share's expiry")
	}
	if err := r.Register(Info{ID: "k", Expires: time.Now().Add(time.Hour)}, &fakeHandle{}); err != nil {
		t.Fatalf("register after tombstone expiry: %v", err)
	}
}

// TestRedialKeepsExpiry: a redial can refresh a share but never push
// its expiry later; an earlier expiry is taken as-is.
func TestRedialKeepsExpiry(t *testing.T) {
	r := NewRegistry(time.Hour)
	defer r.Stop()

	first := time.Now().Add(time.Hour)
	r.Register(Info{ID: "s", Expires: first}, &fakeHandle{})
	r.Register(Info{ID: "s", Expires: first.Add(time.Hour)}, &fakeHandle{})
	info, _, err := r.Lookup("s")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Expires.Equal(first) {
		t.Fatalf("redial moved expiry to %s, want %s", info.Expires, first)
	}

	earlier := first.Add(-30 * time.Minute)
	r.Register(Info{ID: "s", Expires: earlier}, &fakeHandle{})
	info, _, _ = r.Lookup("s")
	if !info.Expires.Equal(earlier) {
		t.Fatalf("earlier expiry not taken: %s, want %s", info.Expires, earlier)
	}
}

// TestKillAccount: every live share of one account is killed and
// tombstoned; other accounts' shares and anonymous ones are untouched.
func TestKillAccount(t *testing.T) {
	r := NewRegistry(time.Hour)
	defer r.Stop()
	soon := time.Now().Add(time.Hour)
	r.Register(Info{ID: "m1", Account: "mallory", Expires: soon}, &fakeHandle{})
	r.Register(Info{ID: "m2", Account: "mallory", Expires: soon}, &fakeHandle{})
	r.Register(Info{ID: "a1", Account: "alice", Expires: soon}, &fakeHandle{})
	r.Register(Info{ID: "x1", Expires: soon}, &fakeHandle{})

	killed := r.KillAccount("mallory")
	if len(killed) != 2 {
		t.Fatalf("killed %v, want m1 and m2", killed)
	}
	for _, id := range []string{"m1", "m2"} {
		if _, _, err := r.Lookup(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s still live: %v", id, err)
		}
		if err := r.Register(Info{ID: id, Account: "mallory", Expires: soon}, &fakeHandle{}); !errors.Is(err, ErrKilled) {
			t.Errorf("%s re-registered after KillAccount: %v", id, err)
		}
	}
	for _, id := range []string{"a1", "x1"} {
		if _, _, err := r.Lookup(id); err != nil {
			t.Errorf("%s killed by another account's takedown: %v", id, err)
		}
	}
	if got := r.KillAccount(""); got != nil {
		t.Errorf("KillAccount(\"\") = %v, want nothing", got)
	}
}
