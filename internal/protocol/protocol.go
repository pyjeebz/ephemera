// Package protocol defines the frame format spoken between daemon
// and relay over the WebSocket bridge.
//
// Wire format (little-endian), v0:
//
//	magic uint32 = 'E' | 'P'<<8 | 'H'<<16 | 'R'<<24   (0x52485045, "EPHR" LE)
//	type  uint8    frame type, one of the T* constants
//	len   uint32   payload length in bytes
//	payload        JSON body (for TData: raw bytes, no JSON)
//
// TData carries raw file bytes. Everything else carries a JSON body;
// each type's payload struct is defined in frames.go. Unknown types
// decode to ErrUnknownType so old relays can skip new frames forward.
package protocol

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Frame types.
const (
	THello uint8 = 1 // daemon -> relay: identity, version
	TSend  uint8 = 2 // daemon -> relay: announce a share
	TRead  uint8 = 3 // relay -> daemon: ask for bytes
	TData  uint8 = 4 // daemon -> relay: raw bytes answering a TRead
	TErr   uint8 = 5 // either: stream aborted, with reason
)

// Frame payload size limits.
const (
	// MaxBody is the largest payload a single frame may carry. TData
	// chunks are sized to it; reads larger than one frame travel as
	// multiple consecutive TData frames for the same TRead.
	MaxBody = 1 << 20

	// MaxMsgLen is the cap for JSON (non-data) frames. Same value as
	// MaxBody today; separate so they can diverge.
	MaxMsgLen = 1 << 20

	// hdrSize is the frame header size: magic(4) + type(1) + len(4).
	hdrSize = 9
)

// magic marks the start of every frame: "EPHR" little-endian.
const magic = uint32(0x52485045)

// Errors.
var (
	ErrBadMagic     = errors.New("protocol: bad magic")
	ErrTooLarge     = errors.New("protocol: frame too large")
	ErrUnknownType  = errors.New("protocol: unknown frame type")
	ErrUnexpected   = errors.New("protocol: unexpected frame type")
	ErrShortPayload = errors.New("protocol: short payload")
)

// Frame is one decoded message.
type Frame struct {
	Type uint8
	JSON []byte // payload for non-data frames (JSON)
	Data []byte // payload for TData (raw bytes)
}

// EncodeJSON marshals v and writes a framed message of the given type.
func EncodeJSON(w io.Writer, typ uint8, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("protocol: marshal: %w", err)
	}
	if len(payload) > MaxMsgLen {
		return ErrTooLarge
	}
	return writeFrame(w, typ, payload)
}

// EncodeData writes a TData frame with raw bytes.
func EncodeData(w io.Writer, data []byte) error {
	if len(data) > MaxBody {
		return ErrTooLarge
	}
	return writeFrame(w, TData, data)
}

func writeFrame(w io.Writer, typ uint8, payload []byte) error {
	var hdr [hdrSize]byte
	binary.LittleEndian.PutUint32(hdr[0:4], magic)
	hdr[4] = typ
	binary.LittleEndian.PutUint32(hdr[5:9], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads one frame from a buffered stream. The returned
// payload aliases a fresh buffer per frame and is safe to hold onto.
func ReadFrame(r *bufio.Reader) (*Frame, error) {
	var hdr [hdrSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(hdr[0:4]) != magic {
		return nil, ErrBadMagic
	}
	typ := hdr[4]
	n := binary.LittleEndian.Uint32(hdr[5:9])
	if n > MaxBody {
		return nil, ErrTooLarge
	}
	f := &Frame{Type: typ}
	if n == 0 {
		return f, nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	switch typ {
	case TData:
		f.Data = buf
	case THello, TSend, TRead, TErr:
		f.JSON = buf
	default:
		return nil, ErrUnknownType
	}
	return f, nil
}
