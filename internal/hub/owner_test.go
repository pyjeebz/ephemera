package hub

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// announceOnly writes a hello+send handshake into a fresh pipe pair
// and returns the relay's end, for Accept calls that should refuse,
// plus the daemon's read end to see what the relay said back.
func announceOnly(t *testing.T, ann protocol.Send) (*pipeConn, *bufio.Reader) {
	t.Helper()
	return announceWith(t, protocol.Hello{Version: "0.1"}, ann)
}

// announceWith is announceOnly with a chosen hello (sender token).
func announceWith(t *testing.T, hello protocol.Hello, ann protocol.Send) (*pipeConn, *bufio.Reader) {
	t.Helper()
	relayR, daemonW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	daemonR, relayW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		relayR.Close()
		daemonW.Close()
		daemonR.Close()
		relayW.Close()
	})
	if err := protocol.EncodeJSON(daemonW, protocol.THello, hello); err != nil {
		t.Fatal(err)
	}
	if err := protocol.EncodeJSON(daemonW, protocol.TSend, ann); err != nil {
		t.Fatal(err)
	}
	return &pipeConn{r: relayR, w: relayW}, bufio.NewReader(daemonR)
}

// expectRefusal reads the TErr a refused daemon must get before the
// relay hangs up, and checks its code.
func expectRefusal(t *testing.T, r *bufio.Reader, code string) protocol.Err {
	t.Helper()
	fr, err := protocol.ReadFrame(r)
	if err != nil {
		t.Fatalf("no refusal frame: %v", err)
	}
	var e protocol.Err
	if fr.Type != protocol.TErr || json.Unmarshal(fr.JSON, &e) != nil {
		t.Fatalf("refusal frame type %d, want TErr", fr.Type)
	}
	if e.Code != code || e.Message == "" {
		t.Fatalf("refusal = %+v, want code %q with a reason", e, code)
	}
	return e
}

// TestAcceptRefusesHijack: someone who knows a live link announces
// its id without the owner key. Accept refuses, and the owner's
// connection keeps serving the link.
func TestAcceptRefusesHijack(t *testing.T) {
	const size = int64(4096)
	reg := share.NewRegistry(time.Hour)
	b := &Bridge{Registry: reg}

	_, relayR, relayW, ann := startFakeDaemon(t, size, 3600)
	owner, err := b.Accept(&pipeConn{r: relayR, w: relayW})
	if err != nil {
		t.Fatalf("owner accept: %v", err)
	}
	defer owner.Close()

	attackerKey, _ := protocol.NewOwnerKey()
	for name, key := range map[string]string{
		"own key": attackerKey,
		"no key":  "",
	} {
		hijack := ann
		hijack.OwnerKey = key
		hijack.Name = "evil.exe.mp4"
		conn, daemonR := announceOnly(t, hijack)
		if _, err := b.Accept(conn); !errors.Is(err, ErrNotOwner) {
			t.Fatalf("%s: accept err = %v, want ErrNotOwner", name, err)
		}
		expectRefusal(t, daemonR, "denied")
	}

	info, h, err := reg.Lookup(ann.ShareID)
	if err != nil {
		t.Fatalf("lookup after hijack attempts: %v", err)
	}
	if h != owner || info.Name != "v.mp4" {
		t.Fatalf("share taken over: name %q, handle is owner: %v", info.Name, h == owner)
	}
	data, err := owner.IssueRead(100, 10)
	if err != nil || len(data) != 10 || data[0] != byte(100%251) {
		t.Fatalf("owner read after hijack attempts: len %d err %v", len(data), err)
	}
}

// TestAcceptRefusesKilledShare: an admin kill sticks even against the
// owner's own redial, which still holds a valid key.
func TestAcceptRefusesKilledShare(t *testing.T) {
	reg := share.NewRegistry(time.Hour)
	b := &Bridge{Registry: reg}

	_, relayR, relayW, ann := startFakeDaemon(t, 4096, 3600)
	d, err := b.Accept(&pipeConn{r: relayR, w: relayW})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer d.Close()

	if !reg.Kill(ann.ShareID) {
		t.Fatal("kill returned false")
	}
	conn, daemonR := announceOnly(t, ann)
	if _, err := b.Accept(conn); !errors.Is(err, share.ErrKilled) {
		t.Fatalf("redial after kill: err = %v, want share.ErrKilled", err)
	}
	expectRefusal(t, daemonR, "gone")
	if _, _, err := reg.Lookup(ann.ShareID); !errors.Is(err, share.ErrNotFound) {
		t.Fatalf("killed share lookup: %v, want ErrNotFound", err)
	}
}
