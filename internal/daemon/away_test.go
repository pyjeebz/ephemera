package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
)

// fakeAwayRelay plays the relay for the away trickle: it acks every
// TAway+TData pair with the running total.
func TestSessionAwayTrickle(t *testing.T) {
	dir := t.TempDir()
	const size = int64(256 << 10) // 256KB file
	path := makeTestFile(t, dir, "video.mp4", size)
	sh, err := ValidateShare(path, DetectMIME)
	if err != nil {
		t.Fatal(err)
	}
	sh.AwayPrefixBytes = 128 << 10 // away: first 128KB

	relay, daemonIn, daemonOut := newFakeRelay(t)
	sess, err := NewSessionIO(daemonIn, daemonOut, sh, 60)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	stop := make(chan struct{})
	served := make(chan error, 1)
	go func() { served <- sess.Serve(stop) }()

	// fake relay: read frames from the daemon; ack away pairs
	relayRd := bufio.NewReader(relay.daemonToRelayR)
	var mu sync.Mutex
	have := int64(0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			fr, err := protocol.ReadFrame(relayRd)
			if err != nil {
				return
			}
			if fr.Type != protocol.TAway {
				continue // announce etc: ignore
			}
			var a protocol.Away
			if json.Unmarshal(fr.JSON, &a) != nil {
				return
			}
			dataFr, err := protocol.ReadFrame(relayRd)
			if err != nil || dataFr.Type != protocol.TData {
				return
			}
			if a.Offset != have {
				t.Errorf("burst offset %d, want %d", a.Offset, have)
			}
			mu.Lock()
			have += int64(len(dataFr.Data))
			total := have
			mu.Unlock()
			if err := protocol.EncodeJSON(relay.relayToDaemonW, protocol.TAck,
				protocol.Ack{ShareID: a.ShareID, Have: total}); err != nil {
				return
			}
			if total >= sh.AwayPrefixBytes {
				return
			}
		}
	}()

	select {
	case <-done:
		// trickle complete
	case <-time.After(10 * time.Second):
		t.Fatal("away trickle did not finish in 10s")
	}
	mu.Lock()
	got := have
	mu.Unlock()
	if got != sh.AwayPrefixBytes {
		t.Fatalf("prefix stored = %d, want %d", got, sh.AwayPrefixBytes)
	}

	// serve still answers reads while/after the trickle
	if err := protocol.EncodeJSON(relay.relayToDaemonW, protocol.TRead,
		protocol.Read{ShareID: sh.ID, Offset: 0, Length: 16}); err != nil {
		t.Fatal(err)
	}
	fr, err := protocol.ReadFrame(relayRd)
	if err != nil {
		t.Fatal(err)
	}
	if fr.Type != protocol.TData || len(fr.Data) != 16 {
		t.Fatalf("read after trickle: type %d len %d", fr.Type, len(fr.Data))
	}

	close(stop)
	if err := <-served; !errors.Is(err, ErrSessionClosed) {
		t.Errorf("serve returned %v", err)
	}
}
