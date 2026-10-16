package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"ephemera.run/ephemera/internal/wsconn"
)

// DefaultDialTimeout bounds one relay dial, so an unreachable relay
// costs a backoff step instead of a hung share.
const DefaultDialTimeout = 15 * time.Second

// ErrPlaintext refuses an unencrypted connection to a relay off this
// machine: the sender token and owner key would cross the network in
// the clear.
var ErrPlaintext = errors.New("refusing a plaintext connection to a non-local relay")

// RelayURL normalizes a relay endpoint:
//
//	wss://host/daemon  ws://host:port/daemon  -> as given
//	https://host       http://host:port       -> wss://, ws://
//	tcp://host:port                           -> raw framed TCP
//	host[:port]                               -> wss://, or ws:// for a
//	                                             local host
//
// A missing path becomes /daemon. Raw TCP exists for tests and local
// runs; production is WSS.
func RelayURL(endpoint string) (*url.URL, error) {
	if !strings.Contains(endpoint, "://") {
		host := endpoint
		if h, _, err := net.SplitHostPort(endpoint); err == nil {
			host = h
		}
		scheme := "wss"
		if IsLocalHost(host) {
			scheme = "ws"
		}
		endpoint = scheme + "://" + endpoint
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
	case "ws", "wss", "tcp":
	default:
		return nil, fmt.Errorf("relay endpoint %q: unknown scheme %q", endpoint, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("relay endpoint %q: no host", endpoint)
	}
	if u.Scheme != "tcp" && (u.Path == "" || u.Path == "/") {
		u.Path = "/daemon"
	}
	return u, nil
}

// CheckTransport refuses a plaintext relay connection to anything
// but this machine, unless insecure is set on purpose.
func CheckTransport(u *url.URL, insecure bool) error {
	if insecure || u.Scheme == "wss" || u.Scheme == "https" || IsLocalHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%w (%s): use wss:// or https://, or pass -insecure", ErrPlaintext, u.Redacted())
}

// IsLocalHost reports whether host names this machine: localhost or
// a loopback address.
func IsLocalHost(host string) bool {
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Dialer connects to relays. The zero value uses the system's root
// certificates and DefaultDialTimeout.
type Dialer struct {
	// TLS overrides the TLS config for wss:// (tests, private CAs).
	TLS *tls.Config

	// Timeout bounds the dial and WebSocket handshake.
	Timeout time.Duration
}

// Dial returns a connection to a relay endpoint (see RelayURL).
func (d Dialer) Dial(endpoint string) (net.Conn, error) {
	u, err := RelayURL(endpoint)
	if err != nil {
		return nil, err
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	if u.Scheme == "tcp" {
		return net.DialTimeout("tcp", u.Host, timeout)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var opts *websocket.DialOptions
	if d.TLS != nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = d.TLS
		opts = &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}}
	}
	c, _, err := websocket.Dial(ctx, u.String(), opts)
	if err != nil {
		return nil, err
	}
	return wsconn.Wrap(c), nil
}

// DialRelay dials endpoint with the zero Dialer.
func DialRelay(endpoint string) (net.Conn, error) {
	return Dialer{}.Dial(endpoint)
}
