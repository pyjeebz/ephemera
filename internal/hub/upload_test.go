package hub

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
	"ephemera.run/ephemera/internal/upload"
)

// uploadDaemon announces ann over pipes and hands back both of the
// daemon's ends, so a test can send bursts after the announce.
func uploadDaemon(t *testing.T, ann protocol.Send) (*pipeConn, io.Writer, *bufio.Reader) {
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
	protocol.EncodeJSON(daemonW, protocol.THello, protocol.Hello{Version: "0.1"})
	protocol.EncodeJSON(daemonW, protocol.TSend, ann)
	return &pipeConn{r: relayR, w: relayW}, daemonW, bufio.NewReader(daemonR)
}

// nextJSON reads frames until one of type typ and decodes it.
func nextJSON(t *testing.T, r *bufio.Reader, typ uint8, v any) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		for {
			fr, err := protocol.ReadFrame(r)
			if err != nil {
				done <- err
				return
			}
			if fr.Type == typ {
				done <- json.Unmarshal(fr.JSON, v)
				return
			}
		}
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waiting for frame %d: %v", typ, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no frame of type %d within 3s", typ)
	}
}

// TestUploadStoredAndResumed: an upload announce is welcomed with
// upload=true, its bursts land on disk with acks, and a redial is
// welcomed with how much the relay already holds.
func TestUploadStoredAndResumed(t *testing.T) {
	reg := share.NewRegistry(time.Hour)
	st, err := upload.Open(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	b := &Bridge{Registry: reg, Uploads: st}
	ann := newAnnounce(60, 20)
	ann.Upload = true

	conn, daemonW, daemonR := uploadDaemon(t, ann)
	d, err := b.Accept(conn)
	if err != nil {
		t.Fatal(err)
	}
	var w protocol.Welcome
	nextJSON(t, daemonR, protocol.TWelcome, &w)
	if !w.Upload || w.Have != 0 {
		t.Fatalf("welcome = %+v, want upload from 0", w)
	}
	if info, _, _ := reg.Lookup(ann.ShareID); !info.Upload {
		t.Fatal("registry doesn't know the share is uploading")
	}

	protocol.EncodeJSON(daemonW, protocol.TAway, protocol.Away{ShareID: ann.ShareID, Offset: 0})
	protocol.EncodeData(daemonW, []byte("0123456789"))
	var a protocol.Ack
	nextJSON(t, daemonR, protocol.TAck, &a)
	if a.Have != 10 || st.Have(ann.ShareID) != 10 {
		t.Fatalf("ack %d, store %d; want 10", a.Have, st.Have(ann.ShareID))
	}
	d.Close()

	// redial: resume at 10
	conn, daemonW, daemonR = uploadDaemon(t, ann)
	d, err = b.Accept(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	w = protocol.Welcome{}
	nextJSON(t, daemonR, protocol.TWelcome, &w)
	if !w.Upload || w.Have != 10 {
		t.Fatalf("redial welcome = %+v, want upload from 10", w)
	}
	protocol.EncodeJSON(daemonW, protocol.TAway, protocol.Away{ShareID: ann.ShareID, Offset: 10})
	protocol.EncodeData(daemonW, []byte("abcdefghij"))
	nextJSON(t, daemonR, protocol.TAck, &a)
	if a.Have != 20 || !st.Complete(ann.ShareID) {
		t.Fatalf("after the last burst: ack %d, complete %v", a.Have, st.Complete(ann.ShareID))
	}
}

// TestUploadWithoutRoomStreamsLive: no store, or no room in it, is
// not a refusal: the share is accepted live-only and told so.
func TestUploadWithoutRoomStreamsLive(t *testing.T) {
	full, err := upload.Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string]*upload.Store{"no store": nil, "full store": full} {
		reg := share.NewRegistry(time.Hour)
		ann := newAnnounce(60, 20)
		ann.Upload = true
		conn, _, daemonR := uploadDaemon(t, ann)
		d, err := (&Bridge{Registry: reg, Uploads: st}).Accept(conn)
		if err != nil {
			t.Fatalf("%s: accept: %v", name, err)
		}
		var w protocol.Welcome
		nextJSON(t, daemonR, protocol.TWelcome, &w)
		if w.Upload {
			t.Fatalf("%s: welcome = %+v, want no upload", name, w)
		}
		if info, _, err := reg.Lookup(ann.ShareID); err != nil || info.Upload {
			t.Fatalf("%s: share %+v (%v), want live-only", name, info, err)
		}
		d.Close()
	}
}

// TestKilledUploadLeavesNothing: a killed link's redial is refused
// and the reservation it made on the way in is dropped.
func TestKilledUploadLeavesNothing(t *testing.T) {
	reg := share.NewRegistry(time.Hour)
	st, _ := upload.Open(t.TempDir(), 1<<20)
	b := &Bridge{Registry: reg, Uploads: st}
	reg.OnExpire(st.Drop)
	ann := newAnnounce(60, 20)
	ann.Upload = true

	conn, _, _ := uploadDaemon(t, ann)
	d, err := b.Accept(conn)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	reg.Kill(ann.ShareID)

	conn, _, daemonR := uploadDaemon(t, ann)
	if _, err := b.Accept(conn); err == nil {
		t.Fatal("killed link accepted again")
	}
	expectRefusal(t, daemonR, "gone")
	if st.Reserved() != 0 || len(st.Stored()) != 0 {
		t.Fatalf("store after kill: reserved %d, %d shares", st.Reserved(), len(st.Stored()))
	}
}
