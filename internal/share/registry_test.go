package share

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeHandle struct {
	mu     sync.Mutex
	closed bool
	reads  int
}

func (h *fakeHandle) IssueRead(off, length int64) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reads++
	return make([]byte, length), nil
}

func (h *fakeHandle) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
}

func TestRegisterAndLookup(t *testing.T) {
	r := NewRegistry(time.Hour)
	defer r.Stop()

	h := &fakeHandle{}
	r.Register(Info{ID: "abc", Name: "v.mp4", Size: 100, MIME: "video/mp4",
		Expires: time.Now().Add(time.Hour)}, h)

	info, hd, err := r.Lookup("abc")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if info.Name != "v.mp4" || info.Size != 100 || !info.Online {
		t.Errorf("info: %+v", info)
	}
	if hd == nil {
		t.Error("handle is nil")
	}

	_, _, err = r.Lookup("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: err = %v, want ErrNotFound", err)
	}
}

func TestLookupExpiredIsGone(t *testing.T) {
	r := NewRegistry(time.Hour)
	defer r.Stop()

	r.Register(Info{ID: "old", Expires: time.Now().Add(-time.Second)}, &fakeHandle{})
	_, _, err := r.Lookup("old")
	if !errors.Is(err, ErrGone) {
		t.Errorf("err = %v, want ErrGone", err)
	}
}

func TestSweepDetachesAndRemoves(t *testing.T) {
	r := NewRegistry(time.Hour)
	defer r.Stop()

	h := &fakeHandle{}
	r.Register(Info{ID: "dead", Expires: time.Now().Add(-time.Second)}, h)
	r.Register(Info{ID: "live", Expires: time.Now().Add(time.Hour)}, &fakeHandle{})

	r.Sweep()

	if got := r.Count(); got != 1 {
		t.Errorf("count after sweep = %d, want 1", got)
	}
	if !h.closed {
		t.Error("expired share's handle was not closed")
	}
	if _, _, err := r.Lookup("dead"); !errors.Is(err, ErrNotFound) {
		t.Errorf("swept share lookup: err = %v, want ErrNotFound", err)
	}
}

func TestSweeperRemovesOnInterval(t *testing.T) {
	r := NewRegistry(50 * time.Millisecond)
	defer r.Stop()

	r.Register(Info{ID: "tmp", Expires: time.Now().Add(20 * time.Millisecond)}, &fakeHandle{})

	// after expiry + a sweep tick, the share must be gone
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := r.Lookup("tmp"); errors.Is(err, ErrNotFound) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sweeper did not remove the expired share within 2s")
}

func TestRedialReplacesHandle(t *testing.T) {
	r := NewRegistry(time.Hour)
	defer r.Stop()

	old := &fakeHandle{}
	r.Register(Info{ID: "same", Expires: time.Now().Add(time.Hour)}, old)

	// a small grace so the async close in Register lands
	time.Sleep(50 * time.Millisecond)

	fresh := &fakeHandle{}
	r.Register(Info{ID: "same", Name: "v.mp4", Size: 200, Expires: time.Now().Add(2 * time.Hour)}, fresh)

	info, _, err := r.Lookup("same")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 200 {
		t.Errorf("info not refreshed: %+v", info)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		old.mu.Lock()
		closed := old.closed
		old.mu.Unlock()
		if closed {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("old handle was never closed after redial")
}
