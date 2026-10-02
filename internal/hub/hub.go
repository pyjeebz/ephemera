// Package hub is the relay's bridge: one daemon WebSocket
// connection per share, pumping frames between daemon sockets and
// receiver HTTP requests.
//
// Concurrency shape: a per-connection writer goroutine is the only
// writer to the socket (frames are never interleaved); a reader
// goroutine decodes frames and routes data frames to the waiting
// IssueRead caller. Reads are correlated FIFO: daemons answer in
// the order asked, so a single reply queue per connection suffices
// — no stream-id multiplexing in v0.
package hub

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"ephemera.run/ephemera/internal/metrics"
	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// Conn is the hub's view of a daemon connection: a framed byte
// stream in both directions. net.Conn and WS netConn adapters both
// satisfy it.
type Conn interface {
	io.ReadWriteCloser
}

// DaemonConn is one live daemon socket carrying one share.
type DaemonConn struct {
	registry *share.Registry

	shareID string
	ttlSec  int64

	conn   Conn
	reader *bufio.Reader

	// readMu serializes IssueRead calls: the protocol has no
	// request ids in v0, so replies are matched to asks FIFO.
	// Without the lock, two concurrent receiver streams could
	// cross-consume each other's data.
	readMu sync.Mutex

	writeCh chan []byte   // frames to encode and send
	replyCh chan []byte   // data payload per IssueRead, FIFO
	errCh   chan error    // first terminal error wins
	done    chan struct{} // closed exactly once on failure/close

	closeOnce sync.Once
}

// ErrDaemonGone is returned to readers when the daemon connection
// drops mid-request. Callers translate it to the receiver page's
// offline state, not a 410.
var ErrDaemonGone = errors.New("hub: daemon connection gone")

// Accept takes ownership of a freshly connected daemon socket:
// it reads hello + send, registers the share, and starts the
// pump goroutines. Callers hand the socket straight from Accept/Dial.
func Accept(reg *share.Registry, conn Conn) (*DaemonConn, error) {
	d := &DaemonConn{
		registry: reg,
		conn:     conn,
		reader:   bufio.NewReader(conn),
		writeCh:  make(chan []byte, 16),
		replyCh:  make(chan []byte, 4),
		errCh:    make(chan error, 1),
		done:     make(chan struct{}),
	}

	// hello + send arrive before anything else
	if err := d.expectHello(); err != nil {
		conn.Close()
		return nil, err
	}
	info, err := d.expectSend()
	if err != nil {
		conn.Close()
		return nil, err
	}
	d.shareID = info.ShareID
	d.ttlSec = info.TTLSec

	expires := time.Now().Add(time.Duration(info.TTLSec) * time.Second)
	reg.Register(share.Info{
		ID:        info.ShareID,
		Name:      info.Name,
		Size:      info.Size,
		MIME:      info.MIME,
		Expires:   expires,
		Cacheable: info.Cache,
	}, d)

	// thesis receipts: every announcement counts
	metrics.SharesStarted.Add(1)
	metrics.BytesAnnounced.Add(info.Size)
	metrics.UpdateRatio()

	// pump goroutines
	go d.writeLoop()
	go d.readLoop()
	return d, nil
}

// expectHello reads one THello frame.
func (d *DaemonConn) expectHello() error {
	fr, err := protocol.ReadFrame(d.reader)
	if err != nil {
		return fmt.Errorf("hub: hello: %w", err)
	}
	if fr.Type != protocol.THello {
		return fmt.Errorf("hub: first frame type %d, want THello", fr.Type)
	}
	return nil
}

// expectSend reads one TSend frame and decodes it.
func (d *DaemonConn) expectSend() (protocol.Send, error) {
	var s protocol.Send
	fr, err := protocol.ReadFrame(d.reader)
	if err != nil {
		return s, fmt.Errorf("hub: send: %w", err)
	}
	if fr.Type != protocol.TSend {
		return s, fmt.Errorf("hub: second frame type %d, want TSend", fr.Type)
	}
	if err := json.Unmarshal(fr.JSON, &s); err != nil {
		return s, fmt.Errorf("hub: send json: %w", err)
	}
	if s.ShareID == "" || s.Size < 1 {
		return s, fmt.Errorf("hub: malformed send payload")
	}
	return s, nil
}

// writeLoop is the single writer: it owns the socket write side.
func (d *DaemonConn) writeLoop() {
	for {
		select {
		case frame := <-d.writeCh:
			if _, err := d.conn.Write(frame); err != nil {
				d.fail(err)
				return
			}
		case <-d.done:
			return
		}
	}
}

// readLoop decodes frames: TData goes to the FIFO reply queue,
// TErr fails the waiting reader, anything else is skipped.
func (d *DaemonConn) readLoop() {
	for {
		fr, err := protocol.ReadFrame(d.reader)
		if err != nil {
			d.fail(err)
			return
		}
		switch fr.Type {
		case protocol.TData:
			select {
			case d.replyCh <- fr.Data:
			case <-d.done:
				return
			}
		case protocol.TErr:
			var e protocol.Err
			if json.Unmarshal(fr.JSON, &e) != nil {
				e = protocol.Err{Code: "unknown"}
			}
			d.fail(fmt.Errorf("hub: daemon error: %s: %s", e.Code, e.Message))
			return
		default:
			// v0: no other frame types matter
		}
	}
}

// IssueRead asks the daemon for bytes. Blocking; one reply per call.
// Serialized per connection: the reply queue is FIFO, so concurrent
// callers would cross-consume each other's data. The relay layer
// may parallelize across connections, never within one.
func (d *DaemonConn) IssueRead(offset, length int64) ([]byte, error) {
	d.readMu.Lock()
	defer d.readMu.Unlock()
	rd := protocol.Read{ShareID: d.shareID, Offset: offset, Length: length}
	frame, err := encodeReadFrame(rd)
	if err != nil {
		return nil, err
	}

	select {
	case d.writeCh <- frame:
	case err := <-d.errCh:
		return nil, err
	case <-d.done:
		return nil, ErrDaemonGone
	}

	var out []byte
	remaining := length
	for remaining > 0 {
		select {
		case data := <-d.replyCh:
			out = append(out, data...)
			remaining -= int64(len(data))
		case err := <-d.errCh:
			return nil, err
		case <-d.done:
			return nil, ErrDaemonGone
		}
	}
	return out, nil
}

// encodeReadFrame marshals and frames a TRead without touching the
// socket — the writeLoop does the writing.
func encodeReadFrame(rd protocol.Read) ([]byte, error) {
	payload, err := json.Marshal(rd)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 9+len(payload))
	buf[0] = 'E'
	buf[1] = 'P'
	buf[2] = 'H'
	buf[3] = 'R'
	buf[4] = protocol.TRead
	le := uint32le(len(payload))
	buf[5] = byte(le)
	buf[6] = byte(le >> 8)
	buf[7] = byte(le >> 16)
	buf[8] = byte(le >> 24)
	copy(buf[9:], payload)
	return buf, nil
}

func uint32le(n int) uint32 { return uint32(n) }

// fail records the terminal error and closes the socket. Exactly
// once: closes done (waking both pumps and all IssueRead callers),
// publishes the error, and detaches from the registry so the share
// reads as offline until the daemon redials.
func (d *DaemonConn) fail(cause error) {
	d.closeOnce.Do(func() {
		close(d.done)
		// first terminal error wins; buffer 1, never blocks
		select {
		case d.errCh <- ErrDaemonGone:
		default:
		}
		d.conn.Close()
		if d.registry != nil && d.shareID != "" {
			d.registry.Detach(d.shareID, d)
		}
	})
	_ = cause
}

// Close shuts the connection down and detaches the share. Safe to
// call multiple times.
func (d *DaemonConn) Close() {
	d.fail(io.EOF)
}
