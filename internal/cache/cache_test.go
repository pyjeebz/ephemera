package cache

import (
	"fmt"
	"sync"
	"testing"
)

func TestPutGetLRU(t *testing.T) {
	s := New(1 << 20)

	s.Put("a", 0, []byte("hello"))
	s.Put("a", 5, []byte("world"))

	got := s.Get("a", 0, 5)
	if string(got) != "hello" {
		t.Fatalf("get = %q", got)
	}
	if s.Get("a", 5, 5) == nil {
		t.Fatal("second chunk missing")
	}
	// wrong length: miss (exact-match contract)
	if s.Get("a", 0, 4) != nil {
		t.Error("partial-length hit should miss")
	}
	// other share: miss
	if s.Get("b", 0, 5) != nil {
		t.Error("cross-share hit")
	}
}

func TestBudgetEviction(t *testing.T) {
	s := New(110) // 10 chunks of 11B

	for i := 0; i < 12; i++ {
		s.Put("a", int64(i*11), []byte(fmt.Sprintf("chunk-%02d---", i)))
	}
	if s.Held() > 110 {
		t.Fatalf("budget blown: %d", s.Held())
	}
	// oldest two evicted
	if s.Get("a", 0, 11) != nil {
		t.Error("chunk 0 should be evicted")
	}
	if s.Get("a", 11, 11) != nil {
		t.Error("chunk 1 should be evicted")
	}
	// newest two present
	if s.Get("a", 110, 11) == nil || s.Get("a", 121, 11) == nil {
		t.Error("recent chunks evicted")
	}
}

func TestGetRefreshesRecency(t *testing.T) {
	s := New(33) // 3 chunks of 11 bytes
	const L = 11

	s.Put("a", 0, []byte("chunk-0----"))
	s.Put("a", 11, []byte("chunk-1----"))
	s.Get("a", 0, L) // refresh 0
	s.Put("a", 22, []byte("chunk-2----"))

	// evict LRU (1), not (0)
	s.Put("a", 33, []byte("chunk-3----"))
	if s.Get("a", 0, L) == nil {
		t.Error("refreshed chunk was evicted")
	}
	if s.Get("a", 11, L) != nil {
		t.Error("stale chunk survived")
	}
}

func TestChunkLargerThanBudget(t *testing.T) {
	s := New(16)
	s.Put("a", 0, make([]byte, 32))
	if s.Get("a", 0, 32) != nil {
		t.Error("oversize chunk stored")
	}
	if s.Held() != 0 {
		t.Errorf("held = %d", s.Held())
	}
}

func TestDropShare(t *testing.T) {
	s := New(1 << 20)
	s.Put("a", 0, []byte("hello"))
	s.Put("b", 0, []byte("world"))
	s.DropShare("a")
	if s.Get("a", 0, 5) != nil {
		t.Error("share a survived DropShare")
	}
	if s.Get("b", 0, 5) == nil {
		t.Error("share b was collateral damage")
	}
	if s.Held() != 5 {
		t.Errorf("held = %d, want 5", s.Held())
	}
}

func TestDisabledStore(t *testing.T) {
	s := New(0)
	s.Put("a", 0, []byte("x"))
	if s.Get("a", 0, 1) != nil {
		t.Error("disabled store served a hit")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := New(1 << 20)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				data := []byte(fmt.Sprintf("w%d-%03d-----", i, j))
				s.Put("s", int64(j*10), data)
				s.Get("s", int64(j*10), int64(len(data)))
				if j%50 == 0 {
					s.DropShare("other")
				}
			}
		}(i)
	}
	wg.Wait()
}
