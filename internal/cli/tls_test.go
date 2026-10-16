package cli

import (
	"errors"
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
