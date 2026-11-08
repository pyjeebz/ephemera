// Package stream frames a command's output so stdout, stderr and the exit
// code can share one HTTP response body.
//
// Each frame is a 1-byte kind, a 4-byte big-endian payload length, and the
// payload.
package stream

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Frame kinds.
const (
	Stdout byte = 1
	Stderr byte = 2
	Exit   byte = 3 // payload: 4-byte big-endian int32 exit code
	Error  byte = 4 // payload: error message; the command did not finish
)

// ContentType is the media type of a framed stream.
const ContentType = "application/vnd.ephemera.stream"

const (
	maxChunk   = 1 << 20  // largest payload written in one frame
	maxPayload = 16 << 20 // largest payload accepted when reading
)

// Writer writes frames, flushing after each one when the underlying writer
// is an http.ResponseWriter. It is safe for concurrent use.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
	f  http.Flusher
}

// NewWriter returns a Writer on w.
func NewWriter(w io.Writer) *Writer {
	f, _ := w.(http.Flusher)
	return &Writer{w: w, f: f}
}

// Frame writes one frame, splitting large payloads.
func (w *Writer) Frame(kind byte, p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for first := true; first || len(p) > 0; first = false {
		n := min(len(p), maxChunk)
		var hdr [5]byte
		hdr[0] = kind
		binary.BigEndian.PutUint32(hdr[1:], uint32(n))
		if _, err := w.w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.w.Write(p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	if w.f != nil {
		w.f.Flush()
	}
	return nil
}

// Exit writes the exit frame.
func (w *Writer) Exit(code int) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(int32(code)))
	return w.Frame(Exit, b[:])
}

// Fail writes an error frame.
func (w *Writer) Fail(msg string) error {
	return w.Frame(Error, []byte(msg))
}

// To returns an io.Writer that sends everything written to it as frames of
// the given kind.
func (w *Writer) To(kind byte) io.Writer {
	return frameWriter{w: w, kind: kind}
}

type frameWriter struct {
	w    *Writer
	kind byte
}

func (f frameWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := f.w.Frame(f.kind, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Frame is one decoded frame.
type Frame struct {
	Kind byte
	Data []byte
}

// ExitCode returns the exit code carried by an Exit frame.
func (f Frame) ExitCode() int {
	if len(f.Data) != 4 {
		return -1
	}
	return int(int32(binary.BigEndian.Uint32(f.Data)))
}

// Read reads the next frame. It returns io.EOF at a clean end of stream.
func Read(r io.Reader) (Frame, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return Frame{}, fmt.Errorf("stream: truncated frame header")
		}
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxPayload {
		return Frame{}, fmt.Errorf("stream: frame of %d bytes is too large", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return Frame{}, fmt.Errorf("stream: truncated frame: %w", err)
	}
	return Frame{Kind: hdr[0], Data: data}, nil
}
