package hub

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// fakeDaemon plays the sender over a pipe pair: it announces a
// share, then answers TReads from an in-memory buffer.
type fakeDaemon struct {
	mu   sync.Mutex
	data []byte // the "file"

	toRelayW   io.WriteCloser // daemon writes frames here
	fromRelayR io.ReadCloser  // daemon reads frames here

	dead bool // when true, stop answering
}

func startFakeDaemon(t *testing.T, fileSize int64, ttl int64) (*fakeDaemon, io.ReadCloser, io.WriteCloser, protocol.Send) {
	t.Helper()
	fd := &fakeDaemon{data: make([]byte, fileSize)}
	for i := range fd.data {
		fd.data[i] = byte(i % 251)
	}

	// relay reads daemon's frames via relayR; sends via toDaemonW
	relayR, toDaemonW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fromRelayR, relayW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		relayR.Close()
		toDaemonW.Close()
		fromRelayR.Close()
		relayW.Close()
	})

	fd.toRelayW = toDaemonW
	fd.fromRelayR = fromRelayR

	key, id := protocol.NewOwnerKey()
	ann := protocol.Send{
		ShareID:  id,
		OwnerKey: key,
		Name:     "v.mp4",
		Size:     fileSize,
		MIME:     "video/mp4",
		TTLSec:   ttl,
	}

	// announce: hello + send
	if err := protocol.EncodeJSON(fd.toRelayW, protocol.THello, protocol.Hello{Version: "0.1"}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.EncodeJSON(fd.toRelayW, protocol.TSend, ann); err != nil {
		t.Fatal(err)
	}

	// answer reads in the background
	go fd.serve()
	return fd, relayR, relayW, ann
}

func (fd *fakeDaemon) serve() {
	br := bufio.NewReader(fd.fromRelayR)
	for {
		fr, err := protocol.ReadFrame(br)
		if err != nil {
			return
		}
		fd.mu.Lock()
		dead := fd.dead
		fd.mu.Unlock()
		if dead || fr.Type != protocol.TRead {
			return
		}
		var rd protocol.Read
		if json.Unmarshal(fr.JSON, &rd) != nil {
			return
		}
		if rd.Offset < 0 || rd.Length < 1 || rd.Offset+rd.Length > int64(len(fd.data)) {
			protocol.EncodeJSON(fd.toRelayW, protocol.TErr, protocol.Err{Code: "denied"})
			continue
		}
		buf := fd.data[rd.Offset : rd.Offset+rd.Length]
		for off := 0; off < len(buf); off += protocol.MaxBody {
			end := off + protocol.MaxBody
			if end > len(buf) {
				end = len(buf)
			}
			if err := protocol.EncodeData(fd.toRelayW, buf[off:end]); err != nil {
				return
			}
		}
	}
}

// TestAcceptRegistersAndServes: end to end over pipes — Accept
// consumes the announce, the registry sees the share, IssueRead
// returns the exact bytes.
func TestAcceptRegistersAndServes(t *testing.T) {
	const size = int64(3<<20 + 7) // crosses MaxBody boundaries
	reg := share.NewRegistry(time.Hour)

	_, relayR, relayW, ann := startFakeDaemon(t, size, 3600)

	d, err := (&Bridge{Registry: reg}).Accept(&pipeConn{r: relayR, w: relayW})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer d.Close()

	info, h, err := reg.Lookup(ann.ShareID)
	if err != nil {
		t.Fatalf("lookup after accept: %v", err)
	}
	if !info.Online || h == nil {
		t.Fatalf("share not online: %+v handle=%v", info, h)
	}
	if info.Size != size || info.Name != "v.mp4" {
		t.Errorf("info mismatch: %+v", info)
	}

	// full read, byte-exact
	data, err := d.IssueRead(0, size)
	if err != nil {
		t.Fatalf("IssueRead: %v", err)
	}
	if int64(len(data)) != size {
		t.Fatalf("read len = %d, want %d", len(data), size)
	}
	for i := range data {
		if data[i] != byte(i%251) {
			t.Fatalf("byte %d = %d", i, data[i])
		}
	}

	// middle range
	data, err = d.IssueRead(1000, 901)
	if err != nil {
		t.Fatalf("IssueRead range: %v", err)
	}
	if len(data) != 901 || data[0] != byte(1000%251) {
		t.Errorf("range mismatch: len %d first %d", len(data), data[0])
	}
}

// TestIssueReadAfterDaemonHangsUp: a daemon that closes mid-session
// fails reads with ErrDaemonGone and the share reads offline.
func TestIssueReadAfterDaemonHangsUp(t *testing.T) {
	const size = int64(1 << 20)
	reg := share.NewRegistry(time.Hour)

	fd, relayR, relayW, ann := startFakeDaemon(t, size, 3600)

	d, err := (&Bridge{Registry: reg}).Accept(&pipeConn{r: relayR, w: relayW})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer d.Close()

	// healthy read first
	if _, err := d.IssueRead(0, 16); err != nil {
		t.Fatalf("healthy read: %v", err)
	}

	// daemon stops answering (no need to kill it: just stop reading
	// its input and close its outputs)
	fd.mu.Lock()
	fd.dead = true
	fd.mu.Unlock()
	fd.toRelayW.Close()
	fd.fromRelayR.Close()

	_, err = d.IssueRead(0, 16)
	if !errors.Is(err, ErrDaemonGone) {
		t.Fatalf("err = %v, want ErrDaemonGone", err)
	}

	// registry must report offline; the flip is async (readLoop
	// notices EOF, fail() detaches), so poll briefly
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, _, err := reg.Lookup(ann.ShareID)
		if err != nil {
			t.Fatalf("post-fail lookup: %v", err)
		}
		if !info.Online {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("share still online 2s after daemon hangup")
}

// TestSequentialReadsFIFO: rapid sequential reads stay correlated —
// a stress for the reply queue.
func TestSequentialReadsFIFO(t *testing.T) {
	const size = int64(512 << 10)
	reg := share.NewRegistry(time.Hour)
	_, relayR, relayW, _ := startFakeDaemon(t, size, 3600)

	d, err := (&Bridge{Registry: reg}).Accept(&pipeConn{r: relayR, w: relayW})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer d.Close()

	for off := int64(0); off < size; off += 4096 {
		data, err := d.IssueRead(off, 4096)
		if err != nil {
			t.Fatalf("read at %d: %v", off, err)
		}
		if len(data) != 4096 {
			t.Fatalf("read at %d: len %d", off, len(data))
		}
		if data[0] != byte(off%251) {
			t.Fatalf("read at %d: first byte %d, want %d", off, data[0], byte(off%251))
		}
	}
}

// TestBadHandshake: garbage first frames are rejected, socket closed.
func TestBadHandshake(t *testing.T) {
	reg := share.NewRegistry(time.Hour)

	// daemon sends send without hello
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := protocol.EncodeJSON(w, protocol.TSend, protocol.Send{ShareID: "x", Size: 1, TTLSec: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Bridge{Registry: reg}).Accept(&pipeConn{r: r, w: w}); err == nil {
		t.Fatal("expected handshake error")
	}

	// daemon sends hello then garbage
	r2, w2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	defer w2.Close()
	protocol.EncodeJSON(w2, protocol.THello, protocol.Hello{Version: "0"})
	w2.Write([]byte("not a frame at all"))
	if _, err := (&Bridge{Registry: reg}).Accept(&pipeConn{r: r2, w: w2}); err == nil {
		t.Fatal("expected send-frame error")
	}

	if got := reg.Count(); got != 0 {
		t.Errorf("registry count = %d, want 0", got)
	}
}

// pipeConn adapts two pipe ends into one ReadWriteCloser.
type pipeConn struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (p *pipeConn) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeConn) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeConn) Close() error {
	p.r.Close()
	return p.w.Close()
}

// silence unused warnings for time import when tests shrink
var _ = time.Second
