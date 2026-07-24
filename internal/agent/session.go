package agent

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// An interactive session carries two kinds of message over one connection: the
// terminal's bytes, and window-resize events. A plain raw stream cannot mix
// them, so once the shell request is sent both sides speak this tiny framing:
//
//	data:   [0x00][uint32 length][length bytes]
//	resize: [0x01][uint16 rows][uint16 cols]
//
// It is what lets resizing the local terminal reach the guest's pty mid-session,
// without a second connection, while keeping the terminal's own bytes untouched.
//
// The types are exported because both halves — the host client and the in-guest
// agent, which are different packages — encode and decode with them.
const (
	FrameData   byte = 0
	FrameResize byte = 1
)

// WinSize is a terminal size.
type WinSize struct {
	Rows uint16
	Cols uint16
}

// SessionWriter frames messages onto a connection. It is safe for concurrent
// use, because on the host the keystroke and resize goroutines both write.
type SessionWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewSessionWriter wraps w for framed writing.
func NewSessionWriter(w io.Writer) *SessionWriter { return &SessionWriter{w: w} }

// WriteData sends a data frame.
func (sw *SessionWriter) WriteData(p []byte) error {
	var hdr [5]byte
	hdr[0] = FrameData
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(p)))

	sw.mu.Lock()
	defer sw.mu.Unlock()
	if _, err := sw.w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := sw.w.Write(p)
	return err
}

// WriteResize sends a resize frame.
func (sw *SessionWriter) WriteResize(ws WinSize) error {
	var buf [5]byte
	buf[0] = FrameResize
	binary.BigEndian.PutUint16(buf[1:], ws.Rows)
	binary.BigEndian.PutUint16(buf[3:], ws.Cols)

	sw.mu.Lock()
	defer sw.mu.Unlock()
	_, err := sw.w.Write(buf[:])
	return err
}

// SessionReader reads frames from a connection. The bufio.Reader matters: the
// stream may already hold bytes read past the JSON request, and this is where
// they are consumed rather than lost — the same care the vsock handshake needs.
type SessionReader struct {
	r *bufio.Reader
}

// NewSessionReader wraps r for framed reading.
func NewSessionReader(r io.Reader) *SessionReader {
	if br, ok := r.(*bufio.Reader); ok {
		return &SessionReader{r: br}
	}
	return &SessionReader{r: bufio.NewReader(r)}
}

// Next returns the next frame: kind FrameData with data set, or kind FrameResize
// with ws set.
func (sr *SessionReader) Next() (kind byte, data []byte, ws WinSize, err error) {
	k, err := sr.r.ReadByte()
	if err != nil {
		return 0, nil, WinSize{}, err
	}
	switch k {
	case FrameData:
		var lb [4]byte
		if _, err = io.ReadFull(sr.r, lb[:]); err != nil {
			return 0, nil, WinSize{}, err
		}
		data = make([]byte, binary.BigEndian.Uint32(lb[:]))
		if _, err = io.ReadFull(sr.r, data); err != nil {
			return 0, nil, WinSize{}, err
		}
		return FrameData, data, WinSize{}, nil
	case FrameResize:
		var rb [4]byte
		if _, err = io.ReadFull(sr.r, rb[:]); err != nil {
			return 0, nil, WinSize{}, err
		}
		ws = WinSize{Rows: binary.BigEndian.Uint16(rb[:2]), Cols: binary.BigEndian.Uint16(rb[2:])}
		return FrameResize, nil, ws, nil
	default:
		return k, nil, WinSize{}, fmt.Errorf("agent: unknown session frame kind %d", k)
	}
}
