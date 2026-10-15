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
