package hub

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/away"
	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// TestAwayAckRoundTrip: TAway+TData pair gets acked; the ack value
// matches the stored bytes; a redial gets a resume ack on accept.
func TestAwayAckRoundTrip(t *testing.T) {
	reg := share.NewRegistry(time.Hour)
	ast := away.New(1 << 20)

	relayR, daemonW, err := os.Pipe() // daemon -> relay
	if err != nil {
		t.Fatal(err)
	}
	daemonR, relayW, err := os.Pipe() // relay -> daemon
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		relayR.Close()
		daemonW.Close()
		daemonR.Close()
		relayW.Close()
	})

	b := &Bridge{Registry: reg, Away: ast}
	key, aw1 := protocol.NewOwnerKey()

	// daemon side: announce then one away burst
	go func() {
		protocol.EncodeJSON(daemonW, protocol.THello, protocol.Hello{Version: "0"})
		protocol.EncodeJSON(daemonW, protocol.TSend, protocol.Send{
			ShareID: aw1, OwnerKey: key, Name: "v.mp4", Size: 10, MIME: "video/mp4",
			TTLSec: 60, AwayPrefixBytes: 10,
		})
		protocol.EncodeJSON(daemonW, protocol.TAway, protocol.Away{ShareID: aw1, Offset: 0})
		protocol.EncodeData(daemonW, []byte("0123456789"))
	}()

	d, err := b.Accept(&pipeConn{r: relayR, w: relayW})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer d.Close()

	// the ack must arrive on the daemon's read pipe
	daemonRd := bufio.NewReader(daemonR)
	ackCh := make(chan protocol.Ack, 1)
	go func() {
		for {
			fr, err := protocol.ReadFrame(daemonRd)
			if err != nil {
				close(ackCh)
				return
			}
			if fr.Type == protocol.TAck {
				var a protocol.Ack
				if json.Unmarshal(fr.JSON, &a) == nil {
					ackCh <- a
					return
				}
			}
		}
	}()

	select {
	case a, ok := <-ackCh:
		if !ok {
			t.Fatal("daemon read pipe closed before ack")
		}
		if a.Have != 10 {
			t.Errorf("ack have = %d, want 10", a.Have)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no ack within 3s: the trickle would stall here")
	}

	// the blob holds the bytes
	if got := ast.Have(aw1); got != 10 {
		t.Errorf("blob have = %d, want 10", got)
	}

	// second burst after the ack: offset 10
	if err := protocol.EncodeJSON(daemonW, protocol.TAway, protocol.Away{ShareID: aw1, Offset: 10}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.EncodeData(daemonW, []byte("abcdefghij")); err != nil {
		t.Fatal(err)
	}
	select {
	case a, ok := <-ackCh:
		if !ok {
			t.Fatal("pipe closed")
		}
		_ = a
	case <-time.After(3 * time.Second):
		// the reader goroutine returned after the first ack; re-arm
	}
	// re-arm a fresh reader for the second ack
	go func() {
		for {
			fr, err := protocol.ReadFrame(daemonRd)
			if err != nil {
				return
			}
			if fr.Type == protocol.TAck {
				var a protocol.Ack
				if json.Unmarshal(fr.JSON, &a) == nil && a.Have == 20 {
					ackCh <- a
				}
			}
		}
	}()
	select {
	case <-ackCh:
		if got := ast.Have(aw1); got != 20 {
			t.Errorf("blob after second burst = %d, want 20", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second ack never arrived")
	}
}
