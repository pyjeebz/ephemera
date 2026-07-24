package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// A tiny server-side WebSocket, just enough to carry a binary RFB stream to the
// browser. ephemera keeps to one dependency and hand-rolls its wire protocols
// (its own Firecracker client, its own vsock handshake); a WebSocket for binary
// framing is small and well-defined enough to belong in that set rather than pull
// in a library. It handles exactly what the desktop needs: the upgrade handshake,
// binary data frames, and ping/close — no extensions, no text, no fragmentation
// games beyond reassembling a continued message.

// wsGUID is the magic value the handshake concatenates with the client key; the
// SHA-1 of the two, base64'd, is the accept token. It is fixed by RFC 6455.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsConn is an upgraded connection carrying binary messages.
type wsConn struct {
	conn net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex // one writer at a time: the pump and a pong reply can race
}

// upgradeWebSocket performs the RFC 6455 handshake and hijacks the connection.
func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return nil, fmt.Errorf("not a websocket upgrade")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, fmt.Errorf("missing Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("connection does not support hijacking")
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return nil, err
	}

	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := io.WriteString(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{conn: conn, r: buf.Reader}, nil
}

// ReadBinary returns the payload of the next data message, transparently
// answering pings and reassembling a message split across continuation frames.
func (c *wsConn) ReadBinary() ([]byte, error) {
	var msg []byte
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x0, 0x1, 0x2: // continuation, text, binary — all data to us
			msg = append(msg, payload...)
			if fin {
				return msg, nil
			}
		case 0x8: // close
			return nil, io.EOF
		case 0x9: // ping -> pong with the same payload
			if err := c.writeFrame(0xA, payload); err != nil {
				return nil, err
			}
		case 0xA: // pong — ignore
		default:
			return nil, fmt.Errorf("websocket: unexpected opcode %#x", opcode)
		}
	}
}

// WriteBinary sends one binary message.
func (c *wsConn) WriteBinary(data []byte) error { return c.writeFrame(0x2, data) }

// Close tears the connection down.
func (c *wsConn) Close() error { return c.conn.Close() }

// readFrame reads a single WebSocket frame. Client frames are always masked; the
// mask is applied in place on the payload before returning it.
func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.r, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	opcode = h[0] & 0x0F
	masked := h[1]&0x80 != 0
	length := uint64(h[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.r, ext[:]); err != nil {
			return
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.r, ext[:]); err != nil {
			return
		}
		length = binary.BigEndian.Uint64(ext[:])
	}

	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.r, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// writeFrame writes one unmasked frame (server frames must not be masked).
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	var head []byte
	b0 := byte(0x80) | opcode // FIN set: we never fragment our own writes
	switch n := len(payload); {
	case n < 126:
		head = []byte{b0, byte(n)}
	case n <= 0xFFFF:
		head = []byte{b0, 126, byte(n >> 8), byte(n)}
	default:
		head = make([]byte, 10)
		head[0], head[1] = b0, 127
		binary.BigEndian.PutUint64(head[2:], uint64(n))
	}
	if _, err := c.conn.Write(head); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}
