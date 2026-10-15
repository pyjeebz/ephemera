package hub

import (
	"errors"
	"os"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// announceOnly writes a hello+send handshake into a fresh pipe pair
// and returns the relay's end, for Accept calls that should refuse.
func announceOnly(t *testing.T, ann protocol.Send) *pipeConn {
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
	if err := protocol.EncodeJSON(daemonW, protocol.THello, protocol.Hello{Version: "0.1"}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.EncodeJSON(daemonW, protocol.TSend, ann); err != nil {
		t.Fatal(err)
	}
	return &pipeConn{r: relayR, w: relayW}
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
		if _, err := b.Accept(announceOnly(t, hijack)); !errors.Is(err, ErrNotOwner) {
			t.Fatalf("%s: accept err = %v, want ErrNotOwner", name, err)
		}
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
	if _, err := b.Accept(announceOnly(t, ann)); !errors.Is(err, share.ErrKilled) {
		t.Fatalf("redial after kill: err = %v, want share.ErrKilled", err)
	}
	if _, _, err := reg.Lookup(ann.ShareID); !errors.Is(err, share.ErrNotFound) {
		t.Fatalf("killed share lookup: %v, want ErrNotFound", err)
	}
}
