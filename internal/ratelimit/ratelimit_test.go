package ratelimit

import (
	"testing"
	"time"
)

func TestBurstThenThrottle(t *testing.T) {
	l := New(100, 5) // 100/s, burst 5

	// burst: first 5 allowed
	for i := 0; i < 5; i++ {
		ok, rem, _ := l.Allow("ip")
		if !ok || rem != 4-i {
			t.Fatalf("burst[%d]: ok=%v rem=%d", i, ok, rem)
		}
	}
	// 6th: refused
	ok, _, resetIn := l.Allow("ip")
	if ok {
		t.Fatal("6th request allowed")
	}
	if resetIn <= 0 {
		t.Errorf("resetIn = %s, want > 0", resetIn)
	}

	// different key unaffected
	ok, _, _ = l.Allow("other")
	if !ok {
		t.Fatal("other key throttled")
	}
}

func TestRefill(t *testing.T) {
	l := New(1000, 1) // fast refill, burst 1
	ok, _, _ := l.Allow("ip")
	if !ok {
		t.Fatal("first")
	}
	ok, _, _ = l.Allow("ip")
	if ok {
		t.Fatal("second should be empty")
	}
	time.Sleep(2 * time.Millisecond) // 2ms at 1000/s = 2 tokens
	ok, _, _ = l.Allow("ip")
	if !ok {
		t.Fatal("refill failed")
	}
}

func TestSweep(t *testing.T) {
	l := New(1, 1)
	l.maxKeys = 2
	_, _, _ = l.Allow("a")
	_, _, _ = l.Allow("b")
	if len(l.buckets) != 2 {
		t.Fatalf("buckets = %d", len(l.buckets))
	}
	// adding a third triggers sweep; a and b are stale if old
	b := l.buckets["a"]
	b.last = time.Now().Add(-time.Hour)
	_, _, _ = l.Allow("c")
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n > 2 {
		t.Errorf("sweep did not run: %d buckets", n)
	}
}
