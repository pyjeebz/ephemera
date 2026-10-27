package cli_test

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBinariesModeration runs the takedown path with real binaries:
// a receiver reports a link, the operator reviews it with
// `ephemera reports` and runs `ephemera kill -suspend`, the sender's
// daemon stops with the reason, and after a relay restart (same
// -data-dir) the report and the suspension are both still there.
func TestBinariesModeration(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping binary E2E")
	}
	ephemeraBin, relayBin := buildBinaries(t)

	video := filepath.Join(t.TempDir(), "clip.mp4")
	os.WriteFile(video, bytes.Repeat([]byte{7}, 64<<10), 0o644)
	tokensFile := filepath.Join(t.TempDir(), "tokens")
	out, err := exec.Command(relayBin, "token", "-file", tokensFile, "mallory").Output()
	if err != nil {
		t.Fatalf("relay token: %v", err)
	}
	token := strings.TrimSpace(string(out))
	dataDir := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	base := "http://" + addr

	startRelay := func() *exec.Cmd {
		cmd := exec.Command(relayBin, "serve", "-addr", addr, "-tokens", tokensFile, "-data-dir", dataDir, "-admin-token", "sekrit")
		logf, _ := os.CreateTemp(t.TempDir(), "relay.log")
		cmd.Stderr = logf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 10*time.Second, func() bool {
			c, err := net.Dial("tcp", addr)
			if err == nil {
				c.Close()
			}
			return err == nil
		}, logf.Name())
		return cmd
	}
	stopRelay := func(cmd *exec.Cmd) { cmd.Process.Kill(); cmd.Wait() }
	senderEnv := append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir(), "HOME="+t.TempDir(), "EPHEMERA_TOKEN="+token)
	admin := func(args ...string) (string, error) {
		cmd := exec.Command(ephemeraBin, append([]string{args[0], "-relay", base, "-token", "sekrit"}, args[1:]...)...)
		cmd.Env = senderEnv
		b, err := cmd.CombinedOutput()
		return string(b), err
	}

	relay := startRelay()
	defer func() { stopRelay(relay) }()

	send := exec.Command(ephemeraBin, "send", "-relay", "ws://"+addr+"/daemon", "-ttl", "120", video)
	send.Env = senderEnv
	sendLog, _ := os.CreateTemp(t.TempDir(), "send.log")
	send.Stderr = sendLog
	send.Stdout = sendLog
	if err := send.Start(); err != nil {
		t.Fatal(err)
	}
	sendDone := make(chan error, 1)
	go func() { sendDone <- send.Wait() }()
	defer send.Process.Kill()

	_, shareID := waitForLink(t, sendLog.Name())

	// a receiver reports it
	resp, err := http.Post(base+"/"+shareID+"/report", "application/json", strings.NewReader(`{"reason":"malware","note":"not a video"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("report: %d", resp.StatusCode)
	}

	// the operator reviews and takes it down
	if out, err := admin("reports"); err != nil || !strings.Contains(out, shareID) || !strings.Contains(out, "mallory") || !strings.Contains(out, "not a video") {
		t.Fatalf("ephemera reports: %v\n%s", err, out)
	}
	if out, err := admin("kill", "-suspend", shareID); err != nil || !strings.Contains(out, "suspended account mallory") {
		t.Fatalf("ephemera kill -suspend: %v\n%s", err, out)
	}

	// the sender's daemon redials, is refused, and stops with the reason
	select {
	case <-sendDone:
		b, _ := os.ReadFile(sendLog.Name())
		if !strings.Contains(string(b), "this sender account is suspended") {
			t.Fatalf("sender stopped without the reason:\n%s", b)
		}
	case <-time.After(15 * time.Second):
		b, _ := os.ReadFile(sendLog.Name())
		t.Fatalf("sender kept running after suspension:\n%s", b)
	}

	// restart: the report and the suspension survive in -data-dir
	stopRelay(relay)
	relay = startRelay()
	if out, err := admin("reports"); err != nil || !strings.Contains(out, shareID) {
		t.Fatalf("reports after restart: %v\n%s", err, out)
	}
	if out, err := admin("suspended"); err != nil || strings.TrimSpace(out) != "mallory" {
		t.Fatalf("suspended after restart: %v\n%s", err, out)
	}
	again := exec.Command(ephemeraBin, "send", "-relay", "ws://"+addr+"/daemon", "-ttl", "60", video)
	again.Env = senderEnv
	b, err := again.CombinedOutput()
	if err == nil || !strings.Contains(string(b), "this sender account is suspended") {
		t.Fatalf("suspended sender after restart: %v\n%s", err, b)
	}
}
