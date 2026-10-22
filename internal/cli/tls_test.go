package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ephemera.run/ephemera/internal/daemon"
)

// TestPlaintextRefused: commands that carry a token refuse plaintext
// to a relay off this machine before touching the network.
func TestPlaintextRefused(t *testing.T) {
	video := filepath.Join(t.TempDir(), "v.mp4")
	os.WriteFile(video, make([]byte, 1024), 0o644)

	for name, err := range map[string]error{
		"send ws":   runSend([]string{"-relay", "ws://relay.example.com/daemon", video}),
		"send tcp":  runSend([]string{"-relay", "tcp://192.168.1.42:7101", video}),
		"resume ws": runResume([]string{"-relay", "http://relay.example.com"}),
		"kill http": runKill([]string{"-relay", "http://relay.example.com", "-token", "admin", "someid"}),
	} {
		if !errors.Is(err, daemon.ErrPlaintext) {
			t.Errorf("%s: err = %v, want ErrPlaintext", name, err)
		}
	}
}

func TestServeTLSFlags(t *testing.T) {
	tokens := filepath.Join(t.TempDir(), "tokens")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"cert without key": {[]string{"-open", "-tls-cert", "c.pem"}, "go together"},
		"key without cert": {[]string{"-open", "-tls-key", "k.pem"}, "go together"},
		"tcp off machine":  {[]string{"-tokens", tokens, "-tcp", "0.0.0.0:7101"}, "plaintext"},
		"tcp all ifaces":   {[]string{"-tokens", tokens, "-tcp", ":7101"}, "plaintext"},
	} {
		if err := runServe(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one mentioning %q", name, err, tc.want)
		}
	}
}

func TestLinkBase(t *testing.T) {
	plain := httptest.NewRequest("GET", "http://192.168.1.42:7100/daemon", nil)
	proxied := httptest.NewRequest("GET", "http://ephemera.run/daemon", nil)
	proxied.Header.Set("X-Forwarded-Proto", "https")
	direct := httptest.NewRequest("GET", "https://ephemera.run/daemon", nil) // r.TLS set

	for name, tc := range map[string]struct {
		public string
		r      *http.Request
		want   string
	}{
		"plain":      {"", plain, "http://192.168.1.42:7100"},
		"tls proxy":  {"", proxied, "https://ephemera.run"},
		"tls direct": {"", direct, "https://ephemera.run"},
		"public url": {"https://links.example.com", plain, "https://links.example.com"},
	} {
		if got := linkBase(tc.public, tc.r); got != tc.want {
			t.Errorf("%s: linkBase = %q, want %q", name, got, tc.want)
		}
	}

	for in, ok := range map[string]bool{
		"":                          true,
		"https://ephemera.run":      true,
		"http://192.168.1.42:7100":  true,
		"https://example.com/share": true,
		"ephemera.run":              false,
		"ftp://ephemera.run":        false,
		"https://ephemera.run/?x=1": false,
	} {
		if err := checkPublicURL(in); (err == nil) != ok {
			t.Errorf("checkPublicURL(%q) = %v, want ok=%v", in, err, ok)
		}
	}
	if dialable(":7100") != "localhost:7100" || dialable("127.0.0.1:7100") != "127.0.0.1:7100" {
		t.Error("dialable wrong")
	}
}

func TestParseProxies(t *testing.T) {
	got, err := parseProxies(" 127.0.0.1, 10.1.2.3/8 ,::1,, 2001:db8::/32")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"127.0.0.1/32", "10.0.0.0/8", "::1/128", "2001:db8::/32"}
	if len(got) != len(want) {
		t.Fatalf("parseProxies = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("proxy %d = %s, want %s", i, got[i], want[i])
		}
	}
	if got, err := parseProxies(""); err != nil || len(got) != 0 {
		t.Errorf("empty: %v, %v; want none", got, err)
	}
	for _, bad := range []string{"localhost", "10.0.0.0/33", "1.2.3"} {
		if _, err := parseProxies(bad); err == nil {
			t.Errorf("parseProxies(%q) accepted", bad)
		}
	}
}
