package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBinariesBackgroundUpload: with a relay that keeps uploads, the
// sender uploads the whole file, says so, and exits on its own; the
// link keeps serving every byte with the sender gone, including after
// the relay restarts. When the link expires, the files are deleted.
func TestBinariesBackgroundUpload(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping binary E2E")
	}
	content := make([]byte, 6<<20+321)
	for i := range content {
		content[i] = byte(i % 251)
	}
	video := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(video, content, 0o644); err != nil {
		t.Fatal(err)
	}
	ephemeraBin, relayBin := buildBinaries(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	tokensFile := filepath.Join(t.TempDir(), "tokens")
	tokenOut, err := exec.Command(relayBin, "token", "-file", tokensFile, "e2e").Output()
	if err != nil {
		t.Fatalf("relay token: %v", err)
	}
	dataDir := t.TempDir()

	startRelay := func() (stop func()) {
		proc := exec.Command(relayBin, "serve", "-addr", addr, "-tokens", tokensFile, "-data-dir", dataDir, "-upload-gb", "1")
		logf, _ := os.CreateTemp(t.TempDir(), "relay.log")
		proc.Stderr = logf
		if err := proc.Start(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 10*time.Second, func() bool {
			c, err := net.Dial("tcp", addr)
			if err == nil {
				c.Close()
			}
			return err == nil
		}, logf.Name())
		return func() {
			proc.Process.Kill()
			proc.Wait()
			logf.Close()
		}
	}
	stopRelay := startRelay()
	defer func() { stopRelay() }()

	const ttl = 8
	send := exec.Command(ephemeraBin, "send", "-json", "-relay", "ws://"+addr+"/daemon", "-ttl", fmt.Sprint(ttl), video)
	send.Env = append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir(), "HOME="+t.TempDir(), "EPHEMERA_TOKEN="+strings.TrimSpace(string(tokenOut)))
	sendLog, _ := os.CreateTemp(t.TempDir(), "send.log")
	send.Stderr = sendLog
	events, _ := os.CreateTemp(t.TempDir(), "send.json")
	send.Stdout = events
	if err := send.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- send.Wait() }()
	defer send.Process.Kill()
	started := time.Now()

	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("sender exited with %v", err)
		}
	case <-time.After(ttl * time.Second / 2):
		b, _ := os.ReadFile(sendLog.Name())
		t.Fatalf("sender still running after it should have uploaded; log:\n%s", b)
	}
	if b, _ := os.ReadFile(sendLog.Name()); !strings.Contains(string(b), "uploaded: the relay has the whole file") {
		t.Fatalf("sender didn't say the upload finished; log:\n%s", b)
	}

	// -json: one event per line on stdout, link first, uploaded last
	b, _ := os.ReadFile(events.Name())
	var seq []string
	var link, fileURL, shareID string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("stdout line %q is not JSON: %v", line, err)
		}
		seq = append(seq, ev["event"].(string))
		if ev["event"] == "link" {
			link, _ = ev["link"].(string)
			fileURL, _ = ev["file_url"].(string)
			shareID, _ = ev["share_id"].(string)
		}
	}
	if len(seq) < 3 || seq[0] != "link" || seq[1] != "upload" || seq[len(seq)-1] != "uploaded" {
		t.Fatalf("events = %v, want link, upload…, uploaded", seq)
	}
	if link != "http://"+addr+"/"+shareID || fileURL != link+"/file" {
		t.Fatalf("link event: link %q, file_url %q, share %q", link, fileURL, shareID)
	}

	base := "http://" + addr + "/" + shareID
	check := func(when string) {
		t.Helper()
		var st struct{ State string }
		resp, err := http.Get(base + "/status")
		if err != nil {
			t.Fatal(err)
		}
		json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if st.State != "available" {
			t.Fatalf("%s: state %q, want available", when, st.State)
		}
		resp, err = http.Get(base + "/file")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !bytes.Equal(got, content) {
			t.Fatalf("%s: GET file = %d, %d bytes", when, resp.StatusCode, len(got))
		}
	}
	check("sender gone")

	stopRelay()
	stopRelay = startRelay()
	check("after a relay restart")

	// expiry deletes the upload, data and metadata both
	time.Sleep(time.Until(started.Add((ttl + 2) * time.Second)))
	// 410 until the sweeper drops it, 404 after
	resp, err := http.Get(base + "/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after expiry: status %d, want the link gone", resp.StatusCode)
	}
	waitFor(t, 3*time.Second, func() bool {
		left, _ := os.ReadDir(filepath.Join(dataDir, "uploads"))
		return len(left) == 0
	}, sendLog.Name())
}
