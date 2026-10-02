package daemon

import (
	"context"
	"net"
	"net/url"
	"strings"

	"github.com/coder/websocket"

	"ephemera.run/ephemera/internal/wsconn"
)

// DialRelay returns a connection to a relay endpoint. Endpoints:
//
//	ws://host:port/daemon   or http://host:port/daemon -> WebSocket
//	tcp://host:port                          -> raw framed TCP
//
// Raw TCP exists for tests and local runs; production is WS.
func DialRelay(endpoint string) (net.Conn, error) {
	if strings.HasPrefix(endpoint, "tcp://") {
		addr := strings.TrimPrefix(endpoint, "tcp://")
		return net.Dial("tcp", addr)
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
		// already right
	default:
		// bare host:port: assume ws
		u, err = url.Parse("ws://" + endpoint)
		if err != nil {
			return nil, err
		}
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/daemon"
	}

	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}
	return wsconn.Wrap(c), nil
}
