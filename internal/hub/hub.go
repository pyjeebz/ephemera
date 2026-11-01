// Package hub is the relay's bridge: one daemon WebSocket
// connection per share, pumping frames between daemon sockets and
// receiver HTTP requests.
//
// Concurrency shape: a per-connection writer goroutine is the only
// writer to the socket (frames are never interleaved); a reader
// goroutine decodes frames and routes data frames to the read they
// answer. Reads are correlated FIFO: daemons answer in the order
// asked, so several reads can be in flight at once and each reply
// goes to the oldest unanswered read. No request ids in v0.
package hub

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"ephemera.run/ephemera/internal/away"
	"ephemera.run/ephemera/internal/metrics"
	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
	"ephemera.run/ephemera/internal/upload"
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
	upload  bool // away bursts go to the disk store, not layer C

	conn   Conn
	reader *bufio.Reader

	// Reads in flight, oldest first. The protocol has no request
	// ids in v0: the daemon answers in the order asked, so a reply
	// belongs to the head of the queue. sendMu keeps the queue in
	// the order read frames reach the writer; qMu guards the queue.
	sendMu sync.Mutex
	qMu    sync.Mutex
	queue  []*pendingRead
	slots  chan struct{} // one per read in flight, up to maxInFlight

	writeCh chan []byte   // frames to encode and send
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

// TTL and Size are the limits in force, defaults filled in.
func (l Limits) TTL() int64  { return cmp.Or(l.MaxTTLSec, DefaultMaxTTLSec) }
func (l Limits) Size() int64 { return cmp.Or(l.MaxSize, DefaultMaxSize) }

// Bridge configures what Accept connects the daemon to. Zero value
// works: no away store means away frames are refused (the daemon
// stops trickling), default limits apply, and the rest runs as
// before.
type Bridge struct {
	Registry *share.Registry
	Away     *away.Store   // nil: layer C disabled
	Uploads  *upload.Store // nil: background uploads refused
	Auth     Authenticator // nil: open relay, every sender anonymous
	Limits   Limits        // relay-wide; an Account's limits override

	// Suspensions, if set, refuses announces from suspended
	// accounts. Anonymous senders (open relay) have no account to
	// suspend.
	Suspensions Suspensions

	// PublicURL is the base receivers open links under, such as
	// https://ephemera.run. Accept welcomes each accepted daemon with
	// its link; empty sends no welcome. AcceptAt overrides it per
	// connection.
	PublicURL string

	// Guests, if set, lets senders without a token share under
	// tighter limits instead of refusing them. Nil: no token, no
	// share (on a relay with an Authenticator).
	Guests *Guests
}

// Guests is how a relay treats senders without a token. A guest's
// share is live-only: the relay keeps none of its bytes, so a guest
// can't use the relay as a file host. A guest's account is named for
// the address it sent from, so the operator can suspend one address
// without touching the rest.
type Guests struct {
	Limits Limits // a guest's caps; zero fields take the relay's

	// Starts, if set, caps how many new shares one address may start;
	// redials of a share it already started don't count.
	Starts Limiter
}

// Limiter is a per-key allowance, such as ratelimit.Limiter.
type Limiter interface {
	Allow(key string) (ok bool, remaining int, resetIn time.Duration)
}

// Accept takes ownership of a freshly connected daemon socket:
// it reads hello + send, reserves room for a background upload if
// asked, registers the share, welcomes the daemon with its link and
// the bytes the relay already holds (its resume point), and starts
// the pump goroutines. Callers hand the socket
// straight from Accept/Dial.
func (b *Bridge) Accept(conn Conn) (*DaemonConn, error) {
	return b.AcceptAt(conn, b.PublicURL)
}

// AcceptAt is Accept with the link base for this connection, for
// relays that derive their public address from the request.
func (b *Bridge) AcceptAt(conn Conn, linkBase string) (*DaemonConn, error) {
	return b.AcceptFrom(conn, linkBase, "")
}

// AcceptFrom is AcceptAt for a sender at a known address: peer is the
// key it is rate limited under (its IP, or its IPv6 /64). Only a
// sender with a peer may share as a guest.
func (b *Bridge) AcceptFrom(conn Conn, linkBase, peer string) (*DaemonConn, error) {
	d := &DaemonConn{
		bridge:  b,
		conn:    conn,
		reader:  bufio.NewReader(conn),
		writeCh: make(chan []byte, 16),
		slots:   make(chan struct{}, maxInFlight),
		done:    make(chan struct{}),
	}

	// hello + send arrive before anything else
	hello, err := d.expectHello()
	if err != nil {
		conn.Close()
		return nil, err
	}
	acct, err := b.authenticate(hello.Token)
	if errors.Is(err, ErrNoToken) && b.Guests != nil && peer != "" {
		acct, err = Account{Name: "guest-" + peer, Limits: b.Guests.Limits, Guest: true}, nil
	}
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
	if err := acct.Limits.Or(b.Limits).check(info); err != nil {
		reason := err.(limitError).reason
		if acct.Guest {
			reason += "; sign in for bigger files and longer links"
		}
		return nil, refuse(conn, info.ShareID, "denied", reason, err)
	}
	if acct.Guest && b.Guests.Starts != nil && b.newShare(info.ShareID) {
		if ok, _, wait := b.Guests.Starts.Allow(peer); !ok {
			reason := fmt.Sprintf("guests can only start a few shares an hour; try again in %s, or sign in", wait.Round(time.Minute).String())
			return nil, refuse(conn, info.ShareID, "denied", reason, ErrOverLimit)
		}
	}
	d.shareID = info.ShareID
	d.ttlSec = info.TTLSec

	expires := time.Now().Add(time.Duration(info.TTLSec) * time.Second)
	welcome := protocol.Welcome{ShareID: info.ShareID}
	if linkBase != "" {
		welcome.Link = strings.TrimSuffix(linkBase, "/") + "/" + info.ShareID
	}

	// a guest's share streams live and nothing else: no upload, no
	// away prefix, no replay cache, so no guest's bytes are ever kept
	// on the relay
	if acct.Guest {
		info.Upload, info.AwayPrefixBytes, info.Cache = false, 0, false
	}

	// a background upload needs room for the whole file up front; a
	// redial gets back what the relay already holds. No room is not
	// a refusal: the share still streams live from the sender.
	if info.Upload && b.Uploads != nil {
		have, err := b.Uploads.Reserve(upload.Meta{
			ID: info.ShareID, Name: info.Name, Size: info.Size, MIME: info.MIME,
			Account: acct.Name, Expires: expires,
		})
		if err == nil {
			d.upload = true
			welcome.Upload, welcome.Have = true, have
		}
	}
	awayPrefix := min(info.AwayPrefixBytes, info.Size)
	if d.upload {
		awayPrefix = 0 // the whole file is coming: no prefix needed
	} else if b.Away != nil && awayPrefix > 0 {
		welcome.Have = b.Away.Have(info.ShareID)
	}

	reg := b.Registry
	if err := reg.Register(share.Info{
		ID:         info.ShareID,
		Name:       info.Name,
		Size:       info.Size,
		MIME:       info.MIME,
		Expires:    expires,
		Cacheable:  info.Cache,
		AwayPrefix: awayPrefix,
		Upload:     d.upload,
		Account:    acct.Name,
	}, d); err != nil {
		if d.upload {
			b.Uploads.Drop(info.ShareID)
		}
		return nil, refuse(conn, info.ShareID, "gone", "this link was taken down", err)
	}
	d.sendJSON(protocol.TWelcome, welcome)

	// thesis receipts: every announcement counts
	metrics.SharesStarted.Add(1)
	metrics.BytesAnnounced.Add(info.Size)
	metrics.UpdateRatio()

	// pump goroutines
	go d.writeLoop()
	go d.readLoop()
	return d, nil
}

// newShare reports whether id is a share the relay hasn't seen, as
// opposed to a redial of one it has.
func (b *Bridge) newShare(id string) bool {
	_, _, err := b.Registry.Lookup(id)
	return errors.Is(err, share.ErrNotFound)
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
	case s.TTLSec > l.TTL():
		return limitError{fmt.Sprintf("ttl %ds is over this relay's %ds limit", s.TTLSec, l.TTL())}
	case s.Size > l.Size():
		return limitError{fmt.Sprintf("%d bytes is over this relay's %d byte limit", s.Size, l.Size())}
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

// readLoop decodes frames: TData answers the oldest read in flight,
// TAway extends the upload or away blob (acking progress), TErr
// fails the oldest read in flight, anything else is skipped.
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
			d.answer(fr.Data, nil)
		case protocol.TAway:
			// layer C: TAway header is immediately followed by its
			// TData bytes (the daemon writes them adjacent under
			// one lock). Consume the pair atomically, even when the
			// header is bad: a data frame left behind would be read
			// as the reply to a receiver's read.
			dataFr, err := protocol.ReadFrame(d.reader)
			if err != nil {
				d.fail(err)
				return
			}
			var a protocol.Away
			if json.Unmarshal(fr.JSON, &a) != nil || a.ShareID != d.shareID {
				continue
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
			err := fmt.Errorf("hub: daemon error: %s: %s", e.Code, e.Message)
			// the daemon refuses a read instead of answering it: that
			// read fails, the others carry on
			if !d.answer(nil, err) {
				d.fail(err)
				return
			}
		default:
			// v0: no other frame types matter
		}
	}
}

// handleAway appends uploaded bytes, to the disk store for a
// background upload or the away blob for a layer C prefix, and acks
// the new length, so a redialing daemon resumes from the ack.
func (d *DaemonConn) handleAway(a protocol.Away, data []byte) {
	var have int64
	switch {
	case d.upload:
		have = d.bridge.Uploads.Put(a.ShareID, a.Offset, data)
	case d.bridge.Away != nil:
		have = d.bridge.Away.Put(a.ShareID, a.Offset, data)
	default:
		return // layer C disabled: never ack; daemon stops quietly
	}
	d.sendJSON(protocol.TAck, protocol.Ack{ShareID: a.ShareID, Have: have})
}

// sendJSON queues a framed JSON message on the writer pump, waiting
// for room: the uploader counts on every ack arriving.
func (d *DaemonConn) sendJSON(typ uint8, v any) {
	buf := &bytes.Buffer{}
	if err := protocol.EncodeJSON(buf, typ, v); err != nil {
		return
	}
	select {
	case d.writeCh <- buf.Bytes():
	case <-d.done:
	}
}

// maxInFlight caps the reads one daemon connection has outstanding:
// enough to hide the round trip to the sender, bounded so the
// replies waiting at the relay stay small (16 × 256 KiB chunks).
const maxInFlight = 16

// pendingRead is one read in flight: the bytes asked for, the bytes
// so far, and where its outcome goes.
type pendingRead struct {
	want int64
	data []byte
	done chan error // buffered 1: the reader loop never waits on it
}

// IssueRead asks the daemon for bytes and waits for them. Safe to
// call concurrently: each call takes its place in the queue as its
// read frame goes out, and the daemon's replies come back in that
// order, so up to maxInFlight reads overlap their round trips.
func (d *DaemonConn) IssueRead(offset, length int64) ([]byte, error) {
	select {
	case d.slots <- struct{}{}:
	case <-d.done:
		return nil, ErrDaemonGone
	}
	defer func() { <-d.slots }()

	frame, err := encodeReadFrame(protocol.Read{ShareID: d.shareID, Offset: offset, Length: length})
	if err != nil {
		return nil, err
	}
	p := &pendingRead{want: length, done: make(chan error, 1)}

	// queue it and send it in one step, so the queue order is the
	// order the daemon sees the reads
	d.sendMu.Lock()
	d.qMu.Lock()
	d.queue = append(d.queue, p)
	d.qMu.Unlock()
	select {
	case d.writeCh <- frame:
	case <-d.done:
		d.sendMu.Unlock()
		return nil, ErrDaemonGone
	}
	d.sendMu.Unlock()

	select {
	case err := <-p.done:
		if err != nil {
			return nil, err
		}
		return p.data, nil
	case <-d.done:
		return nil, ErrDaemonGone
	}
}

// answer gives a reply to the oldest read in flight: data extends it
// and completes it once it has all it asked for; err fails it. It
// reports whether there was a read to answer.
func (d *DaemonConn) answer(data []byte, err error) bool {
	d.qMu.Lock()
	if len(d.queue) == 0 {
		d.qMu.Unlock()
		return false // nothing asked: drop it
	}
	p := d.queue[0]
	if err == nil {
		p.data = append(p.data, data...)
	}
	finished := err != nil || int64(len(p.data)) >= p.want
	if finished {
		d.queue = d.queue[1:]
	}
	d.qMu.Unlock()
	if finished {
		p.done <- err
	}
	return true
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

// fail closes the socket. Exactly once: closes done (waking both
// pumps and every IssueRead caller with ErrDaemonGone), and detaches
// from the registry so the share reads as offline until the daemon
// redials.
func (d *DaemonConn) fail(cause error) {
	d.closeOnce.Do(func() {
		close(d.done)
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
