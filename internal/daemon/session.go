package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"ephemera.run/ephemera/internal/protocol"
)

// Version is the daemon protocol version announced in THello.
const Version = "0.1.0"

// Session is one live connection to a relay carrying one share.
// It owns: announce-on-connect, then a read loop answering TRead
// frames with chunked TData (or TErr).
type Session struct {
	Share  Share
	TTLSec int64

	conn   net.Conn
	writer io.Writer // frame encoder target; conn for TCP, test pipes otherwise
	reader *bufio.Reader
	file   *os.File

	// writeMu serializes frame writes: receiver replies and the
	// away trickle share the socket, and a TAway header must stay
	// adjacent to its TData bytes.
	writeMu sync.Mutex
}

// NewSession opens the relay connection and wraps it for framing.
func NewSession(conn net.Conn, sh Share, ttlSec int64) (*Session, error) {
	f, err := os.Open(sh.Path)
	if err != nil {
		return nil, err
	}
	return &Session{
		Share:  sh,
		TTLSec: ttlSec,
		conn:   conn,
		writer: conn,
		reader: bufio.NewReader(conn),
		file:   f,
	}, nil
}

// NewSessionIO builds a session over raw pipes: the test seam for the
// fake-bridge integration tests. rc is what the daemon reads (frames
// from the relay), wc is where it writes.
func NewSessionIO(rc io.ReadCloser, wc io.WriteCloser, sh Share, ttlSec int64) (*Session, error) {
	f, err := os.Open(sh.Path)
	if err != nil {
		return nil, err
	}
	return &Session{
		Share:  sh,
		TTLSec: ttlSec,
		writer: wc,
		reader: bufio.NewReader(rc),
		file:   f,
	}, nil
}

// Close releases the file handle and, for TCP sessions, the socket.
func (s *Session) Close() error {
	if s.file != nil {
		s.file.Close()
	}
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

// Announce sends THello then TSend: the two frames a relay needs
// before it will route reads to this session.
func (s *Session) Announce() error {
	if err := protocol.EncodeJSON(s.writer, protocol.THello, protocol.Hello{Version: Version}); err != nil {
		return err
	}
	return protocol.EncodeJSON(s.writer, protocol.TSend, protocol.Send{
		ShareID:         s.Share.ID,
		OwnerKey:        s.Share.OwnerKey,
		Name:            s.Share.Name,
		Size:            s.Share.Size,
		MIME:            s.Share.MIME,
		TTLSec:          s.TTLSec,
		Cache:           s.Share.Cache,
		AwayPrefixBytes: s.Share.AwayPrefixBytes,
	})
}

// serveRead answers one TRead: bounded chunked reads from the file,
// TErr for anything invalid. Reads are re-validated against the
// current file size so a file that shrank on disk cannot leak
// out-of-bounds reads the relay may still believe valid.
func (s *Session) serveRead(rd protocol.Read) error {
	st, err := s.file.Stat()
	if err != nil {
		return protocol.EncodeJSON(s.writer, protocol.TErr, protocol.Err{ShareID: rd.ShareID, Code: "io", Message: "stat failed"})
	}
	size := st.Size()
	if rd.ShareID != s.Share.ID {
		return protocol.EncodeJSON(s.writer, protocol.TErr, protocol.Err{ShareID: rd.ShareID, Code: "gone", Message: "unknown share"})
	}
	if rd.Offset < 0 || rd.Length < 1 || rd.Offset+rd.Length > size || rd.Offset+rd.Length > MaxShareSize {
		return protocol.EncodeJSON(s.writer, protocol.TErr, protocol.Err{ShareID: rd.ShareID, Code: "denied", Message: "read out of bounds"})
	}
	buf := make([]byte, rd.Length)
	if _, err := s.file.ReadAt(buf, rd.Offset); err != nil {
		return protocol.EncodeJSON(s.writer, protocol.TErr, protocol.Err{ShareID: rd.ShareID, Code: "io", Message: "read failed"})
	}
	for off := 0; off < len(buf); off += protocol.MaxBody {
		end := off + protocol.MaxBody
		if end > len(buf) {
			end = len(buf)
		}
		if err := protocol.EncodeData(s.writer, buf[off:end]); err != nil {
			return err
		}
	}
	return nil
}

// ErrSessionClosed is returned by Serve when the relay closes the
// connection: the run loop treats it as a reconnect trigger.
var ErrSessionClosed = errors.New("daemon: session closed by relay")

// RefusedError is the relay turning the share down: over its limits,
// a killed link, or an owner key it does not accept. Redialing cannot
// fix it, so Run stops and returns it.
type RefusedError struct {
	Code    string // "denied" or "gone", from the relay's TErr
	Message string
}

func (e *RefusedError) Error() string { return "relay refused the share: " + e.Message }

// Serve reads frames until the connection drops, an IO error occurs,
// or stop fires. TReads get synchronous replies; TAcks steer the
// away trickle; the away upload runs in the background at a pace
// that never competes with receiver reads.
func (s *Session) Serve(stop <-chan struct{}) error {
	type readMsg struct {
		rd      protocol.Read
		ack     int64 // >0: ack for away bytes (rd zero)
		ok      bool
		refused *RefusedError // the relay sent TErr: stop for good
	}
	msgs := make(chan readMsg, 8)
	defer close(msgs)

	// away trickle state: acks move the frontier; pace is fixed and
	// modest. Reads preempt the trickle.
	awayCh := make(chan int64, 1) // latest ack value
	if s.Share.AwayPrefixBytes > 0 {
		go s.trickleAway(stop, awayCh)
	}

	// decoder goroutine: turns frames into parsed reads / acks
	go func() {
		defer func() { recover() }() // writes to msgs after close
		for {
			fr, err := protocol.ReadFrame(s.reader)
			if err != nil {
				msgs <- readMsg{ok: false}
				return
			}
			switch fr.Type {
			case protocol.TRead:
				var rd protocol.Read
				if json.Unmarshal(fr.JSON, &rd) == nil {
					msgs <- readMsg{rd: rd, ok: true}
				}
			case protocol.TErr:
				var e protocol.Err
				if json.Unmarshal(fr.JSON, &e) != nil {
					e = protocol.Err{Code: "unknown", Message: "no reason given"}
				}
				msgs <- readMsg{refused: &RefusedError{Code: e.Code, Message: e.Message}}
				return
			case protocol.TAck:
				var a protocol.Ack
				if json.Unmarshal(fr.JSON, &a) == nil && a.ShareID == s.Share.ID {
					select {
					case awayCh <- a.Have:
					default: // keep the latest
						select {
						case <-awayCh:
						default:
						}
						awayCh <- a.Have
					}
				}
			}
		}
	}()

	for {
		select {
		case <-stop:
			return ErrSessionClosed
		case m := <-msgs:
			if m.refused != nil {
				return m.refused
			}
			if !m.ok {
				return ErrSessionClosed
			}
			s.writeMu.Lock()
			err := s.serveRead(m.rd)
			s.writeMu.Unlock()
			if err != nil {
				return err
			}
		}
	}
}

// awayPace is the prefix trickle's fixed rate: bytes per burst.
// Small enough that reads always win; big enough to finish a 100MB
// prefix in a few minutes of idleness.
const awayBurst = int64(64 << 10) // 64 KiB per burst
const awayPause = 250 * time.Millisecond

// trickleAway uploads the away prefix in bursts. Each burst is two
// adjacent frames under one write lock: TAway (JSON header: offset)
// then TData (the bytes). Each burst waits for its TAck before the
// next; no ack in time: the relay is gone or refusing — stop quietly.
func (s *Session) trickleAway(stop <-chan struct{}, acks chan int64) {
	prefix := min(s.Share.AwayPrefixBytes, s.fileSize())
	if prefix <= 0 {
		return
	}

	have := int64(0)
	// drain any pre-announce ack the relay sent on accept (redial)
	select {
	case have = <-acks:
	default:
	}

	for have < prefix {
		select {
		case <-stop:
			return
		case have = <-acks:
			// frontier moved (ack for our own or earlier uploads)
		default:
		}

		n := min(awayBurst, prefix-have)
		buf := make([]byte, n)
		if _, err := s.file.ReadAt(buf, have); err != nil {
			return
		}

		// header + data adjacent: the hub pairs them by order
		s.writeMu.Lock()
		err := protocol.EncodeJSON(s.writer, protocol.TAway, protocol.Away{ShareID: s.Share.ID, Offset: have})
		if err == nil {
			err = protocol.EncodeData(s.writer, buf)
		}
		s.writeMu.Unlock()
		if err != nil {
			return
		}

		// wait for this burst's ack before continuing
		select {
		case have = <-acks:
		case <-stop:
			return
		case <-time.After(10 * time.Second):
			return // no ack: relay full or gone
		}
	}
}

// fileSize returns the current size of the open file.
func (s *Session) fileSize() int64 {
	st, err := s.file.Stat()
	if err != nil {
		return 0
	}
	return st.Size()
}

// Deadline returns the wall-clock expiry of the share, computed from
// the announce time.
func (s *Session) Deadline() time.Time {
	return time.Now().Add(time.Duration(s.TTLSec) * time.Second)
}

// String renders the session for logs.
func (s *Session) String() string {
	return fmt.Sprintf("session(share=%s name=%q size=%d ttl=%ds)", s.Share.ID, s.Share.Name, s.Share.Size, s.TTLSec)
}
