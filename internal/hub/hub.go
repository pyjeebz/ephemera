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
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"ephemera.run/ephemera/internal/away"
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
	bridge *Bridge

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

// ErrNotOwner is returned by Accept when the announce's owner key
// does not certify its share id: someone who knows a link trying to
// take it over. The socket is closed and the live share untouched.
var ErrNotOwner = errors.New("hub: owner key does not match share id")

// ErrOverLimit is returned by Accept when an announce asks for more
// than the relay allows: a longer TTL or a bigger file.
var ErrOverLimit = errors.New("hub: share over relay limits")

// Default relay limits, used when a Limits field is zero.
const (
	DefaultMaxTTLSec = int64(24 * 60 * 60) // a day
	DefaultMaxSize   = int64(5 << 30)      // 5GB, the daemon's abuse cap
)

// Limits caps what one announce may ask of the relay. Zero fields
// take the defaults, so a zero Bridge is capped, never unlimited.
type Limits struct {
	MaxTTLSec int64 // longest link lifetime, seconds
	MaxSize   int64 // largest share, bytes
}

func (l Limits) maxTTLSec() int64 { return cmp.Or(l.MaxTTLSec, DefaultMaxTTLSec) }
func (l Limits) maxSize() int64   { return cmp.Or(l.MaxSize, DefaultMaxSize) }

// Bridge configures what Accept connects the daemon to. Zero value
// works: no away store means away frames are refused (the daemon
// stops trickling), default limits apply, and the rest runs as
// before.
type Bridge struct {
	Registry *share.Registry
	Away     *away.Store   // nil: layer C disabled
	Auth     Authenticator // nil: open relay, every sender anonymous
	Limits   Limits        // relay-wide; an Account's limits override

	// Suspensions, if set, refuses announces from suspended
	// accounts. Anonymous senders (open relay) have no account to
	// suspend.
	Suspensions Suspensions
}

// Accept takes ownership of a freshly connected daemon socket:
// it reads hello + send, registers the share, acks any away prefix
// the relay already holds (resume hint), and starts the pump
// goroutines. Callers hand the socket straight from Accept/Dial.
func (b *Bridge) Accept(conn Conn) (*DaemonConn, error) {
	d := &DaemonConn{
		bridge:  b,
		conn:    conn,
		reader:  bufio.NewReader(conn),
		writeCh: make(chan []byte, 16),
		replyCh: make(chan []byte, 4),
		errCh:   make(chan error, 1),
		done:    make(chan struct{}),
	}

	// hello + send arrive before anything else
	hello, err := d.expectHello()
	if err != nil {
		conn.Close()
		return nil, err
	}
	acct, err := b.authenticate(hello.Token)
	switch {
	case errors.Is(err, ErrNoToken):
		return nil, refuse(conn, "", "denied", "this relay needs a sender token: run `ephemera login`", err)
	case errors.Is(err, ErrBadToken):
		return nil, refuse(conn, "", "denied", "sender token not accepted: run `ephemera login` with a valid token", err)
	case err != nil:
		// transient (accounts service down): no refusal, the
		// daemon redials and tries again
		conn.Close()
		return nil, err
	}
	if acct.Name != "" && b.Suspensions != nil && b.Suspensions.Suspended(acct.Name) {
		return nil, refuse(conn, "", "denied", "this sender account is suspended", ErrSuspended)
	}
	info, err := d.expectSend()
	if errors.Is(err, ErrNotOwner) {
		return nil, refuse(conn, info.ShareID, "denied", "this link belongs to another sender", err)
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := acct.Limits.or(b.Limits).check(info); err != nil {
		return nil, refuse(conn, info.ShareID, "denied", err.(limitError).reason, err)
	}
	d.shareID = info.ShareID
	d.ttlSec = info.TTLSec

	expires := time.Now().Add(time.Duration(info.TTLSec) * time.Second)
	reg := b.Registry
	if err := reg.Register(share.Info{
		ID:         info.ShareID,
		Name:       info.Name,
		Size:       info.Size,
		MIME:       info.MIME,
		Expires:    expires,
		Cacheable:  info.Cache,
		AwayPrefix: min(info.AwayPrefixBytes, info.Size),
		Account:    acct.Name,
	}, d); err != nil {
		return nil, refuse(conn, info.ShareID, "gone", "this link was taken down", err)
	}

	// resume hint: if the relay already holds prefix bytes for this
	// share (daemon redial), tell the daemon where to continue
	if b.Away != nil && info.AwayPrefixBytes > 0 {
		if have := b.Away.Have(info.ShareID); have > 0 {
			d.sendJSON(protocol.TAck, protocol.Ack{ShareID: info.ShareID, Have: have})
		}
	}

	// thesis receipts: every announcement counts
	metrics.SharesStarted.Add(1)
	metrics.BytesAnnounced.Add(info.Size)
	metrics.UpdateRatio()

	// pump goroutines
	go d.writeLoop()
	go d.readLoop()
	return d, nil
}

// limitError is an ErrOverLimit with a reason the sender can act on.
type limitError struct{ reason string }

func (e limitError) Error() string        { return ErrOverLimit.Error() + ": " + e.reason }
func (e limitError) Is(target error) bool { return target == ErrOverLimit }

// check refuses an announce over the relay's limits.
func (l Limits) check(s protocol.Send) error {
	switch {
	case s.TTLSec < 1:
		return limitError{"ttl must be at least 1s"}
	case s.TTLSec > l.maxTTLSec():
		return limitError{fmt.Sprintf("ttl %ds is over this relay's %ds limit", s.TTLSec, l.maxTTLSec())}
	case s.Size > l.maxSize():
		return limitError{fmt.Sprintf("%d bytes is over this relay's %d byte limit", s.Size, l.maxSize())}
	}
	return nil
}

// refuse tells a daemon why its announce was turned down, so it can
// stop instead of redialing, then closes the socket. Best-effort: the
// reason is advisory, the refusal is not. Returns cause.
func refuse(conn Conn, shareID, code, reason string, cause error) error {
	_ = protocol.EncodeJSON(conn, protocol.TErr, protocol.Err{ShareID: shareID, Code: code, Message: reason})
	conn.Close()
	return cause
}

// expectHello reads one THello frame and decodes it.
func (d *DaemonConn) expectHello() (protocol.Hello, error) {
	var h protocol.Hello
	fr, err := protocol.ReadFrame(d.reader)
	if err != nil {
		return h, fmt.Errorf("hub: hello: %w", err)
	}
	if fr.Type != protocol.THello {
		return h, fmt.Errorf("hub: first frame type %d, want THello", fr.Type)
	}
	if err := json.Unmarshal(fr.JSON, &h); err != nil {
		return h, fmt.Errorf("hub: hello json: %w", err)
	}
	return h, nil
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
	if !protocol.OwnsShare(s.ShareID, s.OwnerKey) {
		return s, ErrNotOwner
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
// TAway extends the away blob (acking progress), TErr fails the
// waiting reader, anything else is skipped.
func (d *DaemonConn) readLoop() {
	for {
		fr, err := protocol.ReadFrame(d.reader)
		if errors.Is(err, protocol.ErrUnknownType) {
			continue // a newer daemon's frame: skip it, stay connected
		}
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
		case protocol.TAway:
			// layer C: TAway header is immediately followed by its
			// TData bytes (the daemon writes them adjacent under
			// one lock). Consume the pair atomically.
			var a protocol.Away
			if json.Unmarshal(fr.JSON, &a) != nil || a.ShareID != d.shareID {
				continue
			}
			dataFr, err := protocol.ReadFrame(d.reader)
			if err != nil {
				d.fail(err)
				return
			}
			if dataFr.Type != protocol.TData {
				continue // protocol violation: drop, trickle stalls on no-ack
			}
			d.handleAway(a, dataFr.Data)
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

// handleAway appends prefix bytes to the away blob and acks the
// new length, so a redialing daemon resumes from the ack.
func (d *DaemonConn) handleAway(a protocol.Away, data []byte) {
	st := d.bridge.Away
	if st == nil {
		return // layer C disabled: never ack; daemon stops quietly
	}
	have := st.Put(a.ShareID, a.Offset, data)
	d.sendJSON(protocol.TAck, protocol.Ack{ShareID: a.ShareID, Have: have})
}

// sendJSON queues a framed JSON message on the writer pump.
func (d *DaemonConn) sendJSON(typ uint8, v any) {
	buf := &bytes.Buffer{}
	if err := protocol.EncodeJSON(buf, typ, v); err != nil {
		return
	}
	select {
	case d.writeCh <- buf.Bytes():
	case <-d.done:
	default:
		// pump backlog full: the ack is advisory, drop it
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
		if d.bridge != nil && d.bridge.Registry != nil && d.shareID != "" {
			d.bridge.Registry.Detach(d.shareID, d)
		}
	})
	_ = cause
}

// Close shuts the connection down and detaches the share. Safe to
// call multiple times.
func (d *DaemonConn) Close() {
	d.fail(io.EOF)
}
