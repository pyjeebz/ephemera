package daemon

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRelayURL(t *testing.T) {
	for in, want := range map[string]string{
		"wss://relay.example.com/daemon": "wss://relay.example.com/daemon",
		"https://relay.example.com":      "wss://relay.example.com/daemon",
		"http://127.0.0.1:7100":          "ws://127.0.0.1:7100/daemon",
		"ws://localhost:7100/daemon":     "ws://localhost:7100/daemon",
		"tcp://127.0.0.1:7101":           "tcp://127.0.0.1:7101",
		"relay.example.com":              "wss://relay.example.com/daemon",
		"relay.example.com:8443":         "wss://relay.example.com:8443/daemon",
		"localhost:7100":                 "ws://localhost:7100/daemon",
		"127.0.0.1:7100":                 "ws://127.0.0.1:7100/daemon",
		"[::1]:7100":                     "ws://[::1]:7100/daemon",
	} {
		u, err := RelayURL(in)
		if err != nil {
			t.Errorf("RelayURL(%q): %v", in, err)
			continue
		}
		if u.String() != want {
			t.Errorf("RelayURL(%q) = %q, want %q", in, u, want)
		}
	}
	for _, bad := range []string{"ftp://relay.example.com", "wss:///daemon"} {
		if _, err := RelayURL(bad); err == nil {
			t.Errorf("RelayURL(%q): want an error", bad)
		}
	}
}

// TestCheckTransport: plaintext is fine to this machine and refused
// anywhere else unless insecure is set on purpose.
func TestCheckTransport(t *testing.T) {
	for in, ok := range map[string]bool{
		"wss://relay.example.com":     true,
		"ws://localhost:7100":         true,
		"ws://127.0.0.1:7100":         true,
		"ws://127.5.6.7:7100":         true,
		"ws://[::1]:7100":             true,
		"tcp://127.0.0.1:7101":        true,
		"ws://relay.example.com":      false,
		"http://192.168.1.42:7100":    false,
		"tcp://relay.example.com:701": false,
		"ws://localhost.example.com":  false,
	} {
		u, err := RelayURL(in)
		if err != nil {
			t.Fatalf("RelayURL(%q): %v", in, err)
		}
		err = CheckTransport(u, false)
		if ok && err != nil {
			t.Errorf("%s: refused: %v", in, err)
		}
		if !ok && !errors.Is(err, ErrPlaintext) {
			t.Errorf("%s: err = %v, want ErrPlaintext", in, err)
		}
		if err := CheckTransport(u, true); err != nil {
			t.Errorf("%s with insecure: %v", in, err)
		}
	}
}

// TestDialWSS: a wss:// dial verifies the relay's certificate: it
// fails against an untrusted one and works once the CA is trusted.
func TestDialWSS(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.Write(r.Context(), websocket.MessageBinary, []byte("hi"))
		time.Sleep(100 * time.Millisecond)
	}))
	defer ts.Close()
	endpoint := "wss://" + strings.TrimPrefix(ts.URL, "https://") + "/daemon"

	if conn, err := (Dialer{Timeout: 5 * time.Second}).Dial(endpoint); err == nil {
		conn.Close()
		t.Fatal("dial succeeded against an untrusted certificate")
	}

	roots := x509.NewCertPool()
	roots.AddCert(ts.Certificate())
	conn, err := (Dialer{TLS: &tls.Config{RootCAs: roots}, Timeout: 5 * time.Second}).Dial(endpoint)
	if err != nil {
		t.Fatalf("dial with the CA trusted: %v", err)
	}
	defer conn.Close()
	buf := make([]byte, 2)
	if _, err := conn.Read(buf); err != nil || string(buf) != "hi" {
		t.Fatalf("read over wss: %q, %v", buf, err)
	}
}
