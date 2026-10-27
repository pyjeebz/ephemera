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
	"sync/atomic"
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

	// Token is the sender's credential, sent in THello. Empty is
	// fine on open relays.
	Token string

	// OnLink, if set, gets the link from the relay's welcome: the
	// share is announced and receivers can open it.
	OnLink func(link string)

	// OnUpload, if set, follows a background upload: once when the
	// relay welcomes the share, then on every ack.
	OnUpload func(UploadProgress)

	conn   net.Conn
	writer io.Writer // frame encoder target; conn for TCP, test pipes otherwise
	reader *bufio.Reader
	file   *os.File

	// writeMu serializes frame writes: receiver replies and the
	// uploader share the socket, and a TAway header must stay
	// adjacent to its TData bytes.
	writeMu sync.Mutex

	// lastRead is when a receiver read last came in (unix nanos):
	// the uploader keeps out of the way while people are watching.
	lastRead atomic.Int64
}

// UploadProgress is where a background upload stands.
type UploadProgress struct {
	Have, Size int64 // bytes on the relay, of the whole file

	// Refused: the relay isn't keeping this upload (it has no upload
	// storage or no room), so the link streams live only.
	Refused bool
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
	if err := protocol.EncodeJSON(s.writer, protocol.THello, protocol.Hello{Version: Version, Token: s.Token}); err != nil {
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
		Upload:          s.Share.Upload,
	})
}

// serveRead answers one TRead: bounded chunked reads from the file,
// TErr for anything invalid. Reads are re-validated against the
// current file size so a file that shrank on disk cannot leak
// out-of-bounds reads the relay may still believe valid.
func (s *Session) serveRead(rd protocol.Read) error {
	s.lastRead.Store(time.Now().UnixNano())
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

// ErrUploaded is returned by Serve once the relay holds the whole
// file: the link no longer needs this machine, so the share is done.
var ErrUploaded = errors.New("daemon: the whole file is on the relay")

// RefusedError is the relay turning the share down: over its limits,
// a killed link, or an owner key it does not accept. Redialing cannot
// fix it, so Run stops and returns it.
type RefusedError struct {
	Code    string // "denied" or "gone", from the relay's TErr
	Message string
}

func (e *RefusedError) Error() string { return "relay refused the share: " + e.Message }

// Serve reads frames until the connection drops, an IO error occurs,
// stop fires, or a background upload finishes. TReads get synchronous
// replies; the welcome starts the uploader, and TAcks steer it; it
// runs in the background and gives way to receiver reads.
func (s *Session) Serve(stop <-chan struct{}) error {
	type readMsg struct {
		rd      protocol.Read
		ok      bool
		refused *RefusedError // the relay sent TErr: stop for good
	}
	msgs := make(chan readMsg, 8)
	defer close(msgs)

	// quit ends the uploader with this session; uploaded fires when
	// the relay acks the last byte of a background upload
	quit := make(chan struct{})
	defer close(quit)
	uploaded := make(chan struct{})
	acks := make(chan int64, 1) // latest ack value

	// decoder goroutine: turns frames into parsed reads / acks
	go func() {
		defer func() { recover() }() // writes to msgs after close
		for {
			fr, err := protocol.ReadFrame(s.reader)
			if errors.Is(err, protocol.ErrUnknownType) {
				continue // a newer relay's frame: skip it, stay connected
			}
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
			case protocol.TWelcome:
				var w protocol.Welcome
				if json.Unmarshal(fr.JSON, &w) != nil || w.ShareID != s.Share.ID {
					continue
				}
				if w.Link != "" && s.OnLink != nil {
					s.OnLink(w.Link)
				}
				s.startUpload(w, acks, quit, uploaded)
			case protocol.TAck:
				var a protocol.Ack
				if json.Unmarshal(fr.JSON, &a) == nil && a.ShareID == s.Share.ID {
					select {
					case acks <- a.Have:
					default: // keep the latest: acks are cumulative
						select {
						case <-acks:
						default:
						}
						acks <- a.Have
					}
				}
			}
		}
	}()

	for {
		select {
		case <-stop:
			return ErrSessionClosed
		case <-uploaded:
			return ErrUploaded
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

// startUpload starts the uploader the welcome calls for: the whole
// file if the relay took the background upload, else the away prefix
// if one was asked for. It resumes from the bytes the relay holds.
func (s *Session) startUpload(w protocol.Welcome, acks <-chan int64, quit <-chan struct{}, uploaded chan<- struct{}) {
	size := s.fileSize()
	switch {
	case s.Share.Upload && w.Upload:
		s.progress(UploadProgress{Have: w.Have, Size: size})
		go func() {
			report := func(have int64) { s.progress(UploadProgress{Have: have, Size: size}) }
			if s.upload(quit, acks, w.Have, size, report) {
				close(uploaded)
			}
		}()
	case s.Share.Upload:
		s.progress(UploadProgress{Refused: true})
		fallthrough
	case s.Share.AwayPrefixBytes > 0:
		if prefix := min(s.Share.AwayPrefixBytes, size); prefix > 0 {
			go s.upload(quit, acks, w.Have, prefix, nil)
		}
	}
}

func (s *Session) progress(p UploadProgress) {
	if s.OnUpload != nil {
		s.OnUpload(p)
	}
}

// Uploader pacing. Bytes go out in bursts of uploadChunk with up to a
// window of them unacked, so a long round trip doesn't throttle the
// upload. While receivers are reading, the window drops to a single
// burst: their replies queue behind whatever upload bytes are already
// in flight, and live playback matters more than upload speed.
const (
	uploadChunk      = int64(256 << 10)
	uploadWindow     = 16 * uploadChunk // 4 MiB in flight, nobody watching
	uploadWindowBusy = uploadChunk      // receivers reading
	readingFor       = 2 * time.Second  // "reading" = a read this recently
	uploadAckTimeout = 30 * time.Second // no ack this long: relay gone
)

// upload sends the file's bytes [from, target) to the relay as
// adjacent TAway+TData bursts and reports true once the relay has
// acked target. It stops early when the relay stops taking bytes (an
// ack that doesn't move forward), no ack arrives in time, or quit
// fires; a redial resumes from the relay's welcome. report, if set,
// gets every new ack.
func (s *Session) upload(quit <-chan struct{}, acks <-chan int64, from, target int64, report func(have int64)) bool {
	sent, acked := from, from
	buf := make([]byte, uploadChunk)
	for acked < target {
		window := uploadWindow
		if time.Since(time.Unix(0, s.lastRead.Load())) < readingFor {
			window = uploadWindowBusy
		}
		if sent < target && sent-acked < window {
			n := min(uploadChunk, target-sent)
			if _, err := s.file.ReadAt(buf[:n], sent); err != nil {
				return false
			}
			// header + data adjacent: the hub pairs them by order
			s.writeMu.Lock()
			err := protocol.EncodeJSON(s.writer, protocol.TAway, protocol.Away{ShareID: s.Share.ID, Offset: sent})
			if err == nil {
				err = protocol.EncodeData(s.writer, buf[:n])
			}
			s.writeMu.Unlock()
			if err != nil {
				return false
			}
			sent += n
			select {
			case <-quit:
				return false
			default:
			}
			continue
		}

		select {
		case <-quit:
			return false
		case have := <-acks:
			if have <= acked {
				return false // the relay stopped taking bytes
			}
			acked, sent = have, max(sent, have)
			if report != nil {
				report(acked)
			}
		case <-time.After(uploadAckTimeout):
			return false
		}
	}
	return true
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
