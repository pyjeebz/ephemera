// Package vsock dials into a guest over Firecracker's vsock multiplexer.
//
// Firecracker does not expose the guest's vsock as a socket family on the host.
// Instead it listens on one Unix socket and speaks a small text handshake: the
// host connects, writes "CONNECT <guest_port>\n", and the VMM answers
// "OK <host_port>\n" once it has attached the stream to a guest listener. After
// that line the connection is a transparent byte pipe to the guest.
package vsock

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"
)

// Dial connects to a port inside the guest through the VMM's multiplexer socket.
func Dial(udsPath string, guestPort uint32, timeout time.Duration) (net.Conn, error) {
	c, err := net.DialTimeout("unix", udsPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("vsock: dial %s: %w", udsPath, err)
	}

	if timeout > 0 {
		if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
			c.Close()
			return nil, err
		}
	}

	if _, err := fmt.Fprintf(c, "CONNECT %d\n", guestPort); err != nil {
		c.Close()
		return nil, fmt.Errorf("vsock: send CONNECT: %w", err)
	}

	// The reply may arrive in the same read as guest data, so the buffered
	// reader that consumed it has to stay with the connection.
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("vsock: read CONNECT reply: %w", err)
	}
	if !strings.HasPrefix(line, "OK ") {
		c.Close()
		// Firecracker answers a bare "ERROR..." when nothing is listening on
		// that guest port — normally the agent not being up yet.
		return nil, fmt.Errorf("vsock: guest port %d refused: %s", guestPort, strings.TrimSpace(line))
	}

	if timeout > 0 {
		if err := c.SetDeadline(time.Time{}); err != nil {
			c.Close()
			return nil, err
		}
	}
	return &conn{Conn: c, r: br}, nil
}

// conn reattaches the buffered reader used for the handshake, so bytes it read
// ahead are not lost.
type conn struct {
	net.Conn
	r *bufio.Reader
}

func (c *conn) Read(p []byte) (int, error) { return c.r.Read(p) }
