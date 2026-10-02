package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
)

// fakeRelayIO returns the two ends of a fake bridge: reads the daemon
// will see, and a reader for what the daemon writes back.
type fakeRelay struct {
	relayToDaemonW io.WriteCloser
	daemonToRelayR io.ReadCloser
}

func newFakeRelay(t *testing.T) (*fakeRelay, io.ReadCloser, io.WriteCloser) {
	t.Helper()
	relayToDaemonR, relayToDaemonW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	daemonToRelayR, daemonToRelayW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		relayToDaemonR.Close()
		relayToDaemonW.Close()
		daemonToRelayR.Close()
		daemonToRelayW.Close()
	})
	return &fakeRelay{relayToDaemonW: relayToDaemonW, daemonToRelayR: daemonToRelayR}, relayToDaemonR, daemonToRelayW
}

// makeTestFile writes n bytes of deterministic content: byte value
// cycles 0..250 so offset math is checkable by eye.
func makeTestFile(t *testing.T, dir, name string, n int64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	buf := make([]byte, 64<<10)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	written := int64(0)
	for written < n {
		chunk := int64(len(buf))
		if n-written < chunk {
			chunk = n - written
		}
		if _, err := f.Write(buf[:chunk]); err != nil {
			t.Fatal(err)
		}
		written += chunk
	}
	f.Close()
	return path
}

// readReply reads one TData or TErr frame from the daemon.
func readReply(t *testing.T, r *bufio.Reader) (*protocol.Frame, error) {
	t.Helper()
	return protocol.ReadFrame(r)
}

// announce sends the hello+send handshake as a relay would NOT need
// to: the daemon announces on its own. Here we just consume it.
func expectAnnounce(t *testing.T, r *bufio.Reader, sh Share, ttl int64) {
	t.Helper()
	fr, err := protocol.ReadFrame(r)
	if err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if fr.Type != protocol.THello {
		t.Fatalf("first frame type = %d, want THello", fr.Type)
	}
	var h protocol.Hello
	if err := json.Unmarshal(fr.JSON, &h); err != nil {
		t.Fatalf("hello json: %v", err)
	}
	if h.Version == "" {
		t.Error("hello missing version")
	}

	fr, err = protocol.ReadFrame(r)
	if err != nil {
		t.Fatalf("read send: %v", err)
	}
	if fr.Type != protocol.TSend {
		t.Fatalf("second frame type = %d, want TSend", fr.Type)
	}
	var s protocol.Send
	if err := json.Unmarshal(fr.JSON, &s); err != nil {
		t.Fatalf("send json: %v", err)
	}
	if s.ShareID != sh.ID || s.Name != sh.Name || s.Size != sh.Size || s.MIME != sh.MIME || s.TTLSec != ttl {
		t.Errorf("send mismatch: %+v, want share %+v ttl %d", s, sh, ttl)
	}
}

// ask sends a TRead and returns the concatenated TData reply (or the
// TErr). ask reads up to want bytes; multi-frame reads come back as
// consecutive TData frames.
func ask(t *testing.T, w io.Writer, r *bufio.Reader, shareID string, off, want int64) ([]byte, *protocol.Err, error) {
	t.Helper()
	if err := protocol.EncodeJSON(w, protocol.TRead, protocol.Read{ShareID: shareID, Offset: off, Length: want}); err != nil {
		return nil, nil, err
	}
	var out []byte
	for int64(len(out)) < want {
		fr, err := readReply(t, r)
		if err != nil {
			return nil, nil, err
		}
		switch fr.Type {
		case protocol.TErr:
			var e protocol.Err
			if err := json.Unmarshal(fr.JSON, &e); err != nil {
				return nil, nil, err
			}
			return nil, &e, nil
		case protocol.TData:
			out = append(out, fr.Data...)
		default:
			return nil, nil, errors.New("unexpected frame type")
		}
	}
	return out, nil, nil
}

func TestSessionAnnounceAndServe(t *testing.T) {
	dir := t.TempDir()
	const size = int64(3 << 20) // 3MB: forces multi-frame TData
	path := makeTestFile(t, dir, "video.mp4", size)

	sh, err := ValidateShare(path, DetectMIME)
	if err != nil {
		t.Fatal(err)
	}
	if sh.MIME != "video/mp4" {
		t.Fatalf("mime = %q", sh.MIME)
	}

	relay, daemonIn, daemonOut := newFakeRelay(t)
	sess, err := NewSessionIO(daemonIn, daemonOut, sh, 3600)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.Announce(); err != nil {
		t.Fatal(err)
	}
	relayRd := bufio.NewReader(relay.daemonToRelayR)
	expectAnnounce(t, relayRd, sh, 3600)

	stop := make(chan struct{})
	served := make(chan error, 1)
	go func() { served <- sess.Serve(stop) }()
	defer func() { close(stop); <-served }()

	// read the whole file through the bridge, byte-exact
	data, terr, err := ask(t, relay.relayToDaemonW, relayRd, sh.ID, 0, size)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if terr != nil {
		t.Fatalf("unexpected TErr: %+v", *terr)
	}
	if int64(len(data)) != size {
		t.Fatalf("len = %d, want %d", len(data), size)
	}
	// verify content: byte i must equal byte(i%251) (content function
	// repeats every 64KB, both sides of the pipe use the same buf)
	for i := 0; i < len(data); i++ {
		if data[i] != byte(i%(64<<10)%251) {
			t.Fatalf("byte %d = %d, want %d", i, data[i], byte(i%(64<<10)%251))
		}
	}
}

func TestSessionRangedReads(t *testing.T) {
	dir := t.TempDir()
	const size = int64(256 << 10)
	path := makeTestFile(t, dir, "clip.mp4", size)
	sh, err := ValidateShare(path, DetectMIME)
	if err != nil {
		t.Fatal(err)
	}

	relay, daemonIn, daemonOut := newFakeRelay(t)
	sess, err := NewSessionIO(daemonIn, daemonOut, sh, 60)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	stop := make(chan struct{})
	served := make(chan error, 1)
	go func() { served <- sess.Serve(stop) }()
	defer func() { close(stop); <-served }()

	relayRd := bufio.NewReader(relay.daemonToRelayR)

	// middle range: bytes 100..1000 inclusive
	data, _, err := ask(t, relay.relayToDaemonW, relayRd, sh.ID, 100, 901)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 901 {
		t.Fatalf("len = %d, want 901", len(data))
	}
	for i := range data {
		if data[i] != byte((100+i)%(64<<10)%251) {
			t.Fatalf("byte %d mismatch", 100+i)
		}
	}

	// last byte
	data, _, err = ask(t, relay.relayToDaemonW, relayRd, sh.ID, size-1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 {
		t.Fatalf("len = %d, want 1", len(data))
	}

	// out of bounds: refused with TErr, session survives
	_, terr, err := ask(t, relay.relayToDaemonW, relayRd, sh.ID, size-4, 8)
	if err != nil {
		t.Fatal(err)
	}
	if terr == nil {
		t.Fatal("want TErr for out-of-bounds read")
	}
	if terr.Code != "denied" {
		t.Errorf("code = %q, want denied", terr.Code)
	}

	// wrong share id: refused, session survives
	_, terr, err = ask(t, relay.relayToDaemonW, relayRd, "wrong", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if terr == nil || terr.Code != "gone" {
		t.Fatalf("want TErr gone, got %+v", terr)
	}

	// session still answers after the two rejections
	data, _, err = ask(t, relay.relayToDaemonW, relayRd, sh.ID, 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 16 {
		t.Fatalf("post-rejection len = %d, want 16", len(data))
	}
}

func TestSessionDetectsRelayGone(t *testing.T) {
	dir := t.TempDir()
	path := makeTestFile(t, dir, "clip.mp4", 4096)
	sh, err := ValidateShare(path, DetectMIME)
	if err != nil {
		t.Fatal(err)
	}

	relay, daemonIn, daemonOut := newFakeRelay(t)
	sess, err := NewSessionIO(daemonIn, daemonOut, sh, 60)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	stop := make(chan struct{})
	served := make(chan error, 1)
	go func() { served <- sess.Serve(stop) }()

	// relay hangs up
	relay.relayToDaemonW.Close()

	select {
	case err := <-served:
		if !errors.Is(err, ErrSessionClosed) {
			t.Errorf("err = %v, want ErrSessionClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not notice the relay hanging up")
	}
	close(stop)
}
