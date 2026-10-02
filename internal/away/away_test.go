package away

import (
	"sync"
	"testing"
)

func TestPutAppendAndRead(t *testing.T) {
	s := New(1 << 20)

	if got := s.Put("a", 0, []byte("hello")); got != 5 {
		t.Fatalf("put0 ack = %d", got)
	}
	if got := s.Put("a", 5, []byte(" world")); got != 11 {
		t.Fatalf("put5 ack = %d", got)
	}

	if b := s.Read("a", 0, 11); string(b) != "hello world" {
		t.Fatalf("read = %q", b)
	}
	if b := s.Read("a", 6, 5); string(b) != "world" {
		t.Fatalf("slice = %q", b)
	}
	// beyond the blob: nil
	if s.Read("a", 0, 12) != nil {
		t.Error("over-read served")
	}
	if s.Read("a", 11, 1) != nil {
		t.Error("tail over-read served")
	}
}

func TestOutOfOrderAppend(t *testing.T) {
	s := New(1 << 20)
	s.Put("a", 0, []byte("hello"))
	// gap: not accepted, ack says what we have
	if got := s.Put("a", 10, []byte("xxxx")); got != 5 {
		t.Fatalf("gap ack = %d", got)
	}
	// duplicate: same
	if got := s.Put("a", 0, []byte("yyyy")); got != 5 {
		t.Fatalf("dup ack = %d", got)
	}
	if s.Read("a", 5, 1) != nil {
		t.Error("gap bytes served")
	}
}

func TestBudgetRefusal(t *testing.T) {
	s := New(16)
	s.Put("a", 0, []byte("0123456789")) // 10 of 16
	if got := s.Put("a", 10, []byte("abcdefg")); got != 10 {
		t.Fatalf("over-budget ack = %d, want 10 (refused)", got)
	}
	// within budget still fine
	if got := s.Put("a", 10, []byte("12345")); got != 15 {
		t.Fatalf("in-budget ack = %d", got)
	}
}

func TestDisabledStore(t *testing.T) {
	s := New(0)
	if got := s.Put("a", 0, []byte("x")); got != 0 {
		t.Fatalf("disabled ack = %d", got)
	}
	if s.Read("a", 0, 1) != nil {
		t.Error("disabled store served")
	}
}

func TestDropShare(t *testing.T) {
	s := New(1 << 20)
	s.Put("a", 0, []byte("hello"))
	s.Put("b", 0, []byte("world"))
	s.DropShare("a")
	if s.Read("a", 0, 5) != nil {
		t.Error("a survived drop")
	}
	if s.Held() != 5 {
		t.Errorf("held = %d, want 5", s.Held())
	}
}

func TestConcurrent(t *testing.T) {
	s := New(1 << 20)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				off := s.Have("x")
				s.Put("x", off, []byte("0123456789"))
				s.Read("x", 0, off)
				if j%25 == 0 {
					s.DropShare("y")
				}
			}
		}()
	}
	wg.Wait()
	if s.Held() == 0 {
		t.Error("concurrent appends lost everything")
	}
}
