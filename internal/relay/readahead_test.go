package relay

import (
	"bytes"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/share"
)

// slowHandle is a sender 30ms away: every read takes a round trip.
// It records how many reads it had in flight at once.
type slowHandle struct {
	memHandle
	mu       sync.Mutex
	inFlight int
	most     int
}

func (h *slowHandle) IssueRead(off, length int64) ([]byte, error) {
	h.mu.Lock()
	h.inFlight++
	h.most = max(h.most, h.inFlight)
	h.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	h.mu.Lock()
	h.inFlight--
	h.mu.Unlock()
	return h.memHandle.IssueRead(off, length)
}

// TestStreamReadsAhead: one response keeps several reads in flight,
// so a slow sender's round trips overlap, and the body still comes
// back whole and in order.
func TestStreamReadsAhead(t *testing.T) {
	body := patterned(1 << 20)
	h := &slowHandle{memHandle: memHandle{data: body}}
	reg := share.NewRegistry(time.Hour)
	reg.Register(share.Info{ID: "s", Name: "v.bin", Size: int64(len(body)), MIME: "application/octet-stream", Expires: time.Now().Add(time.Hour)}, h)
	ts := httptest.NewServer(New(ServerConfig{Registry: reg, ChunkSize: 64 << 10}))
	defer ts.Close()

	began := time.Now()
	code, data, _ := get(t, ts.URL+"/s/file", "")
	took := time.Since(began)
	if code != 200 || !bytes.Equal(data, body) {
		t.Fatalf("body: %d, %d bytes, equal %v", code, len(data), bytes.Equal(data, body))
	}
	if h.most != readAhead {
		t.Fatalf("most reads in flight = %d, want %d", h.most, readAhead)
	}
	// 16 chunks at 30ms each: 480ms one at a time, ~120ms four at a time
	if took > 300*time.Millisecond {
		t.Fatalf("1 MiB took %s: round trips didn't overlap", took)
	}
}
