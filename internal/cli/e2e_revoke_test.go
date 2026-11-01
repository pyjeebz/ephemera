package cli_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBinariesRevoke: a sender deletes its own link after send has
// exited; the relay stops serving it and deletes its copy. Another
// machine (no key) can't, and a second revoke says it's gone.
func TestBinariesRevoke(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping binary E2E")
	}
	file := filepath.Join(t.TempDir(), "dump.sql")
	os.WriteFile(file, []byte(strings.Repeat("insert into t values (1);\n", 4000)), 0o644)
	ephemeraBin, relayBin := buildBinaries(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	dataDir := t.TempDir()
	relay := exec.Command(relayBin, "serve", "-addr", addr, "-open", "-data-dir", dataDir, "-upload-gb", "1")
	logf, _ := os.CreateTemp(t.TempDir(), "relay.log")
	relay.Stderr = logf
	if err := relay.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { relay.Process.Kill(); relay.Wait() }()
	waitFor(t, 10*time.Second, func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
		}
		return err == nil
	}, logf.Name())

	home := t.TempDir()
	env := append(os.Environ(), "XDG_CONFIG_HOME="+home, "HOME="+home, "EPHEMERA_RELAY=ws://"+addr+"/daemon")
	run := func(env []string, args ...string) (string, string, error) {
		cmd := exec.Command(ephemeraBin, args...)
		cmd.Env = env
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return strings.TrimSpace(stdout.String()), stderr.String(), err
	}

	// EPHEMERA_RELAY picks the relay; send exits once it's uploaded
	link, logs, err := run(env, "send", "-ttl", "60", file)
	if err != nil || !strings.HasPrefix(link, "http://"+addr+"/") {
		t.Fatalf("send: %v, link %q\n%s", err, link, logs)
	}
	id := link[strings.LastIndex(link, "/")+1:]
	status := func() int {
		resp, err := http.Get(link + "/status")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := status(); got != http.StatusOK {
		t.Fatalf("status before revoke: %d", got)
	}

	// another machine has no key
	other := append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir(), "HOME="+t.TempDir())
	if _, logs, err := run(other, "revoke", link); err == nil || !strings.Contains(logs, "no key") {
		t.Fatalf("revoke without the key: %v\n%s", err, logs)
	}

	out, logs, err := run(env, "revoke", "-json", link+"/file")
	if err != nil {
		t.Fatalf("revoke: %v\n%s", err, logs)
	}
	var ev map[string]any
	if json.Unmarshal([]byte(out), &ev) != nil || ev["event"] != "revoked" || ev["id"] != id || ev["already_gone"] != false {
		t.Fatalf("revoke -json = %q", out)
	}
	if got := status(); got == http.StatusOK {
		t.Fatal("link still served after revoke")
	}
	if left, _ := filepath.Glob(filepath.Join(dataDir, "uploads", id+"*")); len(left) != 0 {
		t.Fatalf("relay kept the upload: %v", left)
	}

	// the key is forgotten too
	if _, logs, err := run(env, "revoke", link); err == nil || !strings.Contains(logs, "no key") {
		t.Fatalf("second revoke: %v\n%s", err, logs)
	}
}
