package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ephemera trust pins a host's key by name.
func TestTrust(t *testing.T) {
	ts, cs := doctorEnv(t)
	startHost(t, cs, ts.URL, "box")
	pins := filepath.Join(filepath.Dir(os.Getenv("EPHEMERA_CONFIG")), "known_hosts.json")

	var out, errOut bytes.Buffer
	if code := realMain([]string{"trust", "box"}, nil, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "Pinned box (host_") {
		t.Fatalf("exit %d, %q %q", code, out.String(), errOut.String())
	}
	if b, err := os.ReadFile(pins); err != nil || !strings.Contains(string(b), `"name": "box"`) {
		t.Errorf("pins = %s, %v", b, err)
	}

	errOut.Reset()
	if code := realMain([]string{"trust", "nope"}, nil, &out, &errOut); code != exitFailure || !strings.Contains(errOut.String(), `no host "nope"`) {
		t.Errorf("unknown host: exit %d, %q", code, errOut.String())
	}
	errOut.Reset()
	if code := realMain([]string{"trust"}, nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "Usage: ephemera trust <host>") {
		t.Errorf("no host: exit %d, %q", code, errOut.String())
	}
}
