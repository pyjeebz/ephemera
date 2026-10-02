package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
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
		ShareID: s.Share.ID,
		Name:    s.Share.Name,
		Size:    s.Share.Size,
		MIME:    s.Share.MIME,
		TTLSec:  s.TTLSec,
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

// Serve reads frames until the connection drops, an IO error occurs,
// or ctx fires. Each TRead gets a synchronous reply.
func (s *Session) Serve(stop <-chan struct{}) error {
	type readMsg struct {
		rd protocol.Read
		ok bool
	}
	msgs := make(chan readMsg, 8)
	defer close(msgs)

	// decoder goroutine: turns frames into parsed reads
	go func() {
		defer func() { recover() }() // writes to msgs after close
		for {
			fr, err := protocol.ReadFrame(s.reader)
			if err != nil {
				msgs <- readMsg{ok: false}
				return
			}
			if fr.Type == protocol.TRead {
				var rd protocol.Read
				if json.Unmarshal(fr.JSON, &rd) == nil {
					msgs <- readMsg{rd: rd, ok: true}
				}
			}
			// other frame types: ignored in v0
		}
	}()

	for {
		select {
		case <-stop:
			return ErrSessionClosed
		case m := <-msgs:
			if !m.ok {
				return ErrSessionClosed
			}
			if err := s.serveRead(m.rd); err != nil {
				return err
			}
		}
	}
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
