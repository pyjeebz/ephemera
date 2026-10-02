// Package wsconn adapts coder/websocket streams to net.Conn, so the
// daemon and hub speak their frame protocol over either TCP or WS
// without knowing the difference.
package wsconn

import (
	"context"
	"io"
	"net"
	"time"

	"github.com/coder/websocket"
)

// Wrap turns an established WS connection into a net.Conn. Close
// sends a normal closure; reads/writes are unbounded so large frame
// writes block until drained, like TCP. Accept should be called with
// the default options; Wrap raises the read limit to match the
// protocol's 1 MiB max frame (MaxBody) plus headroom.
func Wrap(c *websocket.Conn) net.Conn {
	c.SetReadLimit(4 << 20) // protocol.MaxBody + headroom
	return &conn{c: c}
}

type conn struct {
	c *websocket.Conn

	// pending holds unread bytes of the current WS message; Reader
	// is only called when pending is empty, so every message is
	// fully consumed before the next one starts (the library
	// requires this).
	pending []byte
}

func (k *conn) Read(b []byte) (int, error) {
	for len(k.pending) == 0 {
		_, r, err := k.c.Reader(context.Background())
		if err != nil {
			return 0, err
		}
		pending, err := io.ReadAll(r)
		if err != nil {
			return 0, err
		}
		k.pending = pending
	}
	n := copy(b, k.pending)
	k.pending = k.pending[n:]
	return n, nil
}

func (k *conn) Write(b []byte) (int, error) {
	w, err := k.c.Writer(context.Background(), websocket.MessageBinary)
	if err != nil {
		return 0, err
	}
	if _, err := w.Write(b); err != nil {
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (k *conn) Close() error {
	return k.c.Close(websocket.StatusNormalClosure, "")
}

func (k *conn) LocalAddr() net.Addr  { return websocketAddr{} }
func (k *conn) RemoteAddr() net.Addr { return websocketAddr{} }

func (k *conn) SetDeadline(time.Time) error      { return nil }
func (k *conn) SetReadDeadline(time.Time) error  { return nil }
func (k *conn) SetWriteDeadline(time.Time) error { return nil }

type websocketAddr struct{}

func (websocketAddr) Network() string { return "websocket" }
func (websocketAddr) String() string  { return "websocket" }
