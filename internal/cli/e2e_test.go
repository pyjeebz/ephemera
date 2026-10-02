package cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestBinariesEndToEnd builds both real binaries and runs the full
// slice over real sockets on localhost:
//
//	ephemera send -relay ws://127.0.0.1:PORT/daemon file
//	relay serve -addr 127.0.0.1:PORT
//	HTTP client does Range requests against /:id/file
//
// Skipped when `go test` runs without network permissions or when
// the toolchain can't build; it is the phase-5 acceptance test.
func TestBinariesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping binary E2E")
	}

	dir := t.TempDir()
	// test file: 2MB deterministic
	var content []byte
	const size = int64(2 << 20)
	for i := range size {
		content = append(content, byte(int(i)%251))
	}
	video := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(video, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// build binaries
	build := t.TempDir()
	ephemeraBin := filepath.Join(build, "ephemera")
	relayBin := filepath.Join(build, "relay")
	for _, tt := range []struct{ dir, out string }{
		{"./cmd/ephemera", ephemeraBin},
		{"./cmd/relay", relayBin},
	} {
		cmd := exec.Command("go", "build", "-o", tt.out, tt.dir)
		cmd.Dir = "../.."
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("build %s: %v", tt.dir, err)
		}
	}

	// pick relay port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	relayURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	relayWS := fmt.Sprintf("ws://127.0.0.1:%d/daemon", port)

	// start relay
	relayProc := exec.Command(relayBin, "serve", "-addr", fmt.Sprintf("127.0.0.1:%d", port), "-cache-mb", "64", "-away-mb", "64")
	relayLog, _ := os.CreateTemp(t.TempDir(), "relay.log")
	relayProc.Stderr = relayLog
	if err := relayProc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		relayProc.Process.Kill()
		relayProc.Wait()
		relayLog.Close()
	}()

	// wait for relay to listen
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// start daemon
	ttl := 90
	sendProc := exec.Command(ephemeraBin, "send", "-relay", relayWS, "-ttl", strconv.Itoa(ttl), "-cache", "-away", "1", video)
	sendLog, _ := os.CreateTemp(t.TempDir(), "send.log")
	sendProc.Stderr = sendLog
	if err := sendProc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		sendProc.Process.Kill()
		sendProc.Wait()
		sendLog.Close()
	}()

	// scrape the share id from the daemon log: "link: /:id"
	var shareID string
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(sendLog.Name())
		if i := strings.Index(string(b), "link: https://ephemera.run/"); i >= 0 {
			line := string(b)[i:]
			shareID = strings.TrimSpace(strings.TrimPrefix(line[:strings.IndexByte(line, '\n')+1], "link: https://ephemera.run/"))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if shareID == "" {
		body, _ := os.ReadFile(sendLog.Name())
		t.Fatalf("no share id in daemon log:\n%s", body)
	}

	// status: live
	resp, err := http.Get(relayURL + "/" + shareID + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		State string `json:"state"`
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		MIME  string `json:"mime"`
	}
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.State != "live" || st.Name != "video.mp4" || st.Size != size || st.MIME != "video/mp4" {
		t.Fatalf("status: %+v", st)
	}

	// range request: bytes=1000-2000
	req, _ := http.NewRequest("GET", relayURL+"/"+shareID+"/file", nil)
	req.Header.Set("Range", "bytes=1000-2000")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d", resp.StatusCode)
	}
	if len(data) != 1001 {
		t.Fatalf("range len = %d", len(data))
	}
	for i := range data {
		if data[i] != byte((1000+i)%251) {
			t.Fatalf("range byte %d = %d", 1000+i, data[i])
		}
	}

	// suffix range: bytes=-1024
	req, _ = http.NewRequest("GET", relayURL+"/"+shareID+"/file", nil)
	req.Header.Set("Range", "bytes=-1024")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(data) != 1024 || data[0] != byte((size-1024)%251) {
		t.Fatalf("suffix: len %d first %d", len(data), data[0])
	}

	// open-end: bytes=1048576- (forces multi-chunk relay)
	req, _ = http.NewRequest("GET", relayURL+"/"+shareID+"/file", nil)
	req.Header.Set("Range", "bytes=1048576-")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if int64(len(data)) != size-1048576 {
		t.Fatalf("open-end len = %d", len(data))
	}
	if data[0] != byte(1048576%251) {
		t.Fatalf("open-end first byte = %d", data[0])
	}

	// wait for the away trickle: 1MiB prefix, 64KiB bursts with
	// pauses — status.prefix reports what the relay holds
	prefixReady := time.Now().Add(20 * time.Second)
	for time.Now().Before(prefixReady) {
		resp, err = http.Get(relayURL + "/" + shareID + "/status")
		if err == nil {
			var s struct {
				Prefix int64 `json:"prefix"`
				State  string
			}
			json.NewDecoder(resp.Body).Decode(&s)
			resp.Body.Close()
			if s.Prefix >= 1<<20 {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	// kill the daemon -> status flips to offline, file reads 503
	sendProc.Process.Kill()
	sendProc.Wait()
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get(relayURL + "/" + shareID + "/status")
		if err == nil {
			var s struct{ State string }
			json.NewDecoder(resp.Body).Decode(&s)
			resp.Body.Close()
			if s.State == "offline" {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st.State != "live" && st.State != "offline" {
		// only fatal if the flip never happened; the loop above
		// exited via timeout otherwise
		resp, _ = http.Get(relayURL + "/" + shareID + "/status")
		var s struct{ State string }
		json.NewDecoder(resp.Body).Decode(&s)
		resp.Body.Close()
		if s.State != "offline" {
			t.Fatalf("status after daemon kill = %q, want offline", s.State)
		}
	}

	// the ranges read while live were 1000-2000, -1024 and the
	// 1MB open-end: the tail 503s (uncached)…
	req, _ = http.NewRequest("GET", relayURL+"/"+shareID+"/file", nil)
	req.Header.Set("Range", "bytes=2000000-")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("uncached offline range = %d, want 503", resp.StatusCode)
	}

	// …but a previously-watched range replays from relay RAM
	req, _ = http.NewRequest("GET", relayURL+"/"+shareID+"/file", nil)
	req.Header.Set("Range", "bytes=1000-2000")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("replay status = %d, want 206", resp.StatusCode)
	}
	if len(data) != 1001 {
		t.Fatalf("replay len = %d, want 1001", len(data))
	}
	for i := range data {
		if data[i] != byte((1000+i)%251) {
			t.Fatalf("replay byte %d wrong", 1000+i)
		}
	}

	// status reports replay while offline
	resp, err = http.Get(relayURL + "/" + shareID + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var stt struct {
		State  string `json:"state"`
		Replay bool   `json:"replay"`
		Prefix int64  `json:"prefix"`
	}
	json.NewDecoder(resp.Body).Decode(&stt)
	resp.Body.Close()
	if stt.State != "offline" || !stt.Replay {
		t.Errorf("offline status = %+v, want offline+replay", stt)
	}

	// layer C: the away prefix (1MiB) was trickled before the kill.
	// A range inside it serves from the blob; a range past it 503s.
	if stt.Prefix == 0 {
		t.Fatal("away prefix never arrived at the relay")
	}
	req, _ = http.NewRequest("GET", relayURL+"/"+shareID+"/file", nil)
	req.Header.Set("Range", "bytes=0-4095")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || len(data) != 4096 {
		t.Fatalf("away prefix read = %d len %d", resp.StatusCode, len(data))
	}
	for i := range data {
		if data[i] != byte(i%251) {
			t.Fatalf("away prefix byte %d wrong", i)
		}
	}
	req, _ = http.NewRequest("GET", relayURL+"/"+shareID+"/file", nil)
	req.Header.Set("Range", "bytes=1500000-")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("past-prefix read = %d, want 503", resp.StatusCode)
	}

	// unknown share: 404
	resp, err = http.Get(relayURL + "/nonexistent/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown = %d, want 404", resp.StatusCode)
	}
}
