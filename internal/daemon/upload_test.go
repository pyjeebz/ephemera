package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
)

// uploadRelay plays the relay for the uploader: it records every
// TAway+TData burst and acks it through ack, which returns the have
// to send back, or -1 to stay silent.
type uploadRelay struct {
	t    *testing.T
	sess *Session
	w    *os.File

	mu     sync.Mutex
	bursts []protocol.Away
	stored []byte
	reads  chan *protocol.Frame
}

func startUploadRelay(t *testing.T, sh Share, ack func(r *uploadRelay, a protocol.Away, n int) int64) (*uploadRelay, chan error) {
	t.Helper()
	relay, daemonIn, daemonOut := newFakeRelay(t)
	sess, err := NewSessionIO(daemonIn, daemonOut, sh, 60)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	r := &uploadRelay{t: t, sess: sess, w: relay.relayToDaemonW.(*os.File), reads: make(chan *protocol.Frame, 16)}
	go func() {
		rd := bufio.NewReader(relay.daemonToRelayR)
		for {
			fr, err := protocol.ReadFrame(rd)
			if err != nil {
				return
			}
			if fr.Type != protocol.TAway {
				r.reads <- fr
				continue
			}
			var a protocol.Away
			json.Unmarshal(fr.JSON, &a)
			data, err := protocol.ReadFrame(rd)
			if err != nil || data.Type != protocol.TData {
				return
			}
			r.mu.Lock()
			r.bursts = append(r.bursts, a)
			if a.Offset == int64(len(r.stored)) {
				r.stored = append(r.stored, data.Data...)
			}
			r.mu.Unlock()
			if have := ack(r, a, len(data.Data)); have >= 0 {
				protocol.EncodeJSON(r.w, protocol.TAck, protocol.Ack{ShareID: a.ShareID, Have: have})
			}
		}
	}()
	served := make(chan error, 1)
	go func() { served <- sess.Serve(make(chan struct{})) }()
	return r, served
}

// ackAll acks every burst with the bytes stored so far.
func ackAll(r *uploadRelay, _ protocol.Away, _ int) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.stored))
}

func (r *uploadRelay) welcome(w protocol.Welcome) {
	w.ShareID = r.sess.Share.ID
	if err := protocol.EncodeJSON(r.w, protocol.TWelcome, w); err != nil {
		r.t.Fatal(err)
	}
}

func (r *uploadRelay) burstCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bursts)
}

func testShare(t *testing.T, size int64) Share {
	t.Helper()
	sh, err := ValidateShare(makeTestFile(t, t.TempDir(), "video.mp4", size), DetectMIME)
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

func wantFile(t *testing.T, sh Share, got []byte, n int64) {
	t.Helper()
	want, _ := os.ReadFile(sh.Path)
	if !bytes.Equal(got, want[:n]) {
		t.Fatalf("relay holds %d bytes that don't match the file's first %d", len(got), n)
	}
}

// TestSessionAwayTrickle: layer C sends the prefix after the welcome
// and keeps answering reads.
func TestSessionAwayTrickle(t *testing.T) {
	sh := testShare(t, 2<<20)
	sh.AwayPrefixBytes = 1 << 20
	r, served := startUploadRelay(t, sh, ackAll)
	r.welcome(protocol.Welcome{})

	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.stored)
		r.mu.Unlock()
		if n == 1<<20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("prefix stored = %d, want %d", n, 1<<20)
		}
		time.Sleep(10 * time.Millisecond)
	}
	wantFile(t, sh, r.stored, 1<<20)

	protocol.EncodeJSON(r.w, protocol.TRead, protocol.Read{ShareID: sh.ID, Offset: 0, Length: 16})
	select {
	case fr := <-r.reads:
		if fr.Type != protocol.TData || len(fr.Data) != 16 {
			t.Fatalf("read after the prefix: type %d len %d", fr.Type, len(fr.Data))
		}
	case err := <-served:
		t.Fatalf("serve ended after the prefix: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("no reply to a read after the prefix")
	}
}

// TestSessionUploadsWholeFile: a taken upload sends every byte,
// reports progress, and ends the session once the last byte is acked.
func TestSessionUploadsWholeFile(t *testing.T) {
	const size = 3<<20 + 123
	sh := testShare(t, size)
	sh.Upload = true
	var mu sync.Mutex
	var seen []UploadProgress
	r, served := startUploadRelay(t, sh, ackAll)
	r.sess.OnUpload = func(p UploadProgress) {
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
	}
	r.welcome(protocol.Welcome{Upload: true})

	select {
	case err := <-served:
		if !errors.Is(err, ErrUploaded) {
			t.Fatalf("serve = %v, want ErrUploaded", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("upload didn't finish in 10s")
	}
	wantFile(t, sh, r.stored, size)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 || seen[0] != (UploadProgress{Have: 0, Size: size}) || seen[len(seen)-1] != (UploadProgress{Have: size, Size: size}) {
		t.Fatalf("progress = %+v, want 0 then up to %d", seen, size)
	}
}

// TestSessionUploadResumes: the first burst starts at the welcome's
// have, and a welcome saying it's all there ends the session at once.
func TestSessionUploadResumes(t *testing.T) {
	sh := testShare(t, 1<<20)
	sh.Upload = true
	r, served := startUploadRelay(t, sh, func(r *uploadRelay, a protocol.Away, n int) int64 { return a.Offset + int64(n) })
	r.welcome(protocol.Welcome{Upload: true, Have: 300 << 10})
	if err := <-served; !errors.Is(err, ErrUploaded) {
		t.Fatalf("serve = %v", err)
	}
	if r.bursts[0].Offset != 300<<10 {
		t.Fatalf("first burst at %d, want %d", r.bursts[0].Offset, 300<<10)
	}

	r, served = startUploadRelay(t, sh, ackAll)
	r.welcome(protocol.Welcome{Upload: true, Have: 1 << 20})
	select {
	case err := <-served:
		if !errors.Is(err, ErrUploaded) || r.burstCount() != 0 {
			t.Fatalf("serve = %v after %d bursts, want ErrUploaded and none", err, r.burstCount())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an upload the relay already holds didn't end the session")
	}
}

// TestSessionUploadRefused: a relay that won't keep the upload is
// reported, nothing is sent, and the share keeps streaming live.
func TestSessionUploadRefused(t *testing.T) {
	sh := testShare(t, 1<<20)
	sh.Upload = true
	got := make(chan UploadProgress, 1)
	r, served := startUploadRelay(t, sh, ackAll)
	r.sess.OnUpload = func(p UploadProgress) { got <- p }
	r.welcome(protocol.Welcome{})
	if p := <-got; !p.Refused {
		t.Fatalf("progress = %+v, want refused", p)
	}
	protocol.EncodeJSON(r.w, protocol.TRead, protocol.Read{ShareID: sh.ID, Offset: 0, Length: 16})
	select {
	case fr := <-r.reads:
		if fr.Type != protocol.TData {
			t.Fatalf("read reply type %d", fr.Type)
		}
	case err := <-served:
		t.Fatalf("serve ended: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("no reply to a read")
	}
	if n := r.burstCount(); n != 0 {
		t.Fatalf("sent %d bursts to a relay that refused the upload", n)
	}
}

// TestUploadStopsWhenRelayStopsTaking: an ack that doesn't move
// forward (relay full) stops the uploader instead of resending.
func TestUploadStopsWhenRelayStopsTaking(t *testing.T) {
	sh := testShare(t, 8<<20)
	sh.AwayPrefixBytes = 8 << 20
	r, served := startUploadRelay(t, sh, func(*uploadRelay, protocol.Away, int) int64 { return 0 })
	r.welcome(protocol.Welcome{})
	time.Sleep(300 * time.Millisecond)
	n := r.burstCount()
	if n == 0 || int64(n) > uploadWindow/uploadChunk {
		t.Fatalf("%d bursts sent to a relay that took none", n)
	}
	time.Sleep(300 * time.Millisecond)
	if r.burstCount() != n {
		t.Fatal("uploader kept sending after the relay stopped taking bytes")
	}
	select {
	case err := <-served:
		t.Fatalf("serve ended: %v", err)
	default:
	}
}

// TestUploadYieldsToReaders: with nobody reading, a whole window goes
// out before the first ack; right after a read, one burst at a time.
func TestUploadYieldsToReaders(t *testing.T) {
	silent := func(*uploadRelay, protocol.Away, int) int64 { return -1 }
	for name, tc := range map[string]struct {
		read bool
		want int
	}{
		"idle":    {false, int(uploadWindow / uploadChunk)},
		"reading": {true, 1},
	} {
		sh := testShare(t, 8<<20)
		sh.Upload = true
		r, _ := startUploadRelay(t, sh, silent)
		if tc.read {
			protocol.EncodeJSON(r.w, protocol.TRead, protocol.Read{ShareID: sh.ID, Offset: 0, Length: 16})
			<-r.reads
		}
		r.welcome(protocol.Welcome{Upload: true})
		time.Sleep(300 * time.Millisecond)
		if n := r.burstCount(); n != tc.want {
			t.Errorf("%s: %d bursts in flight with no ack, want %d", name, n, tc.want)
		}
	}
}
