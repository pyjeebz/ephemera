package relay

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/hub"
	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// byteSource: deterministic content for fake daemons.
func byteAt(i int64) byte { return byte(i % 251) }

// memHandle is a share.Handle answering reads from memory.
type memHandle struct {
	mu   sync.Mutex
	data []byte
}

func (m *memHandle) IssueRead(off, length int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if off+length > int64(len(m.data)) {
		return nil, io.ErrUnexpectedEOF
	}
	return m.data[off : off+length], nil
}

func (m *memHandle) Close() {}

func newTestServer(t *testing.T, body []byte, online bool) (http.Handler, *share.Registry) {
	t.Helper()
	reg := share.NewRegistry(time.Hour)
	h := &memHandle{data: body}
	reg.Register(share.Info{
		ID:      "abc123",
		Name:    "video.mp4",
		Size:    int64(len(body)),
		MIME:    "video/mp4",
		Expires: time.Now().Add(time.Hour),
	}, h)
	if !online {
		reg.Detach("abc123", h)
	}
	return New(ServerConfig{Registry: reg, ChunkSize: 64 << 10}), reg
}

func TestStatusStates(t *testing.T) {
	body := []byte{1, 2, 3}

	// live
	srv, _ := newTestServer(t, body, true)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/abc123/status")
	if err != nil {
		t.Fatal(err)
	}
	var st statusBody
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.State != "live" || st.Name != "video.mp4" || st.Size != 3 {
		t.Errorf("live status: %+v", st)
	}

	// offline
	srv, _ = newTestServer(t, body, false)
	ts = httptest.NewServer(srv)
	defer ts.Close()
	resp, _ = http.Get(ts.URL + "/abc123/status")
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.State != "offline" {
		t.Errorf("offline status: %+v", st)
	}

	// expired: register, then expire, then sweep removes -> 410 via
	// a fresh registry whose entry is already past
	reg := share.NewRegistry(time.Hour)
	ts2 := httptest.NewServer(New(ServerConfig{Registry: reg}))
	defer ts2.Close()
	reg.Register(share.Info{ID: "gone", Expires: time.Now().Add(-time.Second)}, nil)
	resp, _ = http.Get(ts2.URL + "/gone/status")
	if resp.StatusCode != http.StatusGone {
		t.Errorf("expired status = %d, want 410", resp.StatusCode)
	}
	var st2 statusBody
	json.NewDecoder(resp.Body).Decode(&st2)
	resp.Body.Close()
	if st2.State != "expired" {
		t.Errorf("expired body: %+v", st2)
	}

	// unknown: 404
	resp, _ = http.Get(ts2.URL + "/nope/status")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestFileRangeRequests(t *testing.T) {
	// 1.5MB body: exercises chunked streaming (chunk size 64KB)
	var body []byte
	const size = int64(1536 * 1024)
	for i := int64(0); i < size; i++ {
		body = append(body, byteAt(i))
	}
	srv, _ := newTestServer(t, body, true)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	url := ts.URL + "/abc123/file"

	// 1. bytes=a-b: 206, exact body, correct headers
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Range", "bytes=1000-2000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(data) != 1001 {
		t.Fatalf("len = %d, want 1001", len(data))
	}
	for i := range data {
		if data[i] != byteAt(1000+int64(i)) {
			t.Fatalf("byte %d mismatch", 1000+i)
		}
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 1000-2000/1572864" {
		t.Errorf("Content-Range = %q", cr)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "1001" {
		t.Errorf("Content-Length = %q", cl)
	}

	// 2. bytes=1000-: 206 to EOF
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Range", "bytes=1000-")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || int64(len(data)) != size-1000 {
		t.Fatalf("open-end: %d len %d", resp.StatusCode, len(data))
	}

	// 3. bytes=-1024: last 1024
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Range", "bytes=-1024")
	resp, _ = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(data) != 1024 || data[0] != byteAt(size-1024) {
		t.Errorf("suffix: len %d", len(data))
	}

	// 4. no Range: 200 + full length
	resp, err = http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("no-range status = %d", resp.StatusCode)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "1572864" {
		t.Errorf("full Content-Length = %q", cl)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// 5. HEAD: correct headers, no body
	req, _ = http.NewRequest("HEAD", url, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Length") != "1572864" {
		t.Errorf("HEAD: %d CL=%q", resp.StatusCode, resp.Header.Get("Content-Length"))
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges missing")
	}

	// 7. start >= size: 416 with Content-Range bytes */size
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Range", "bytes=9999999-")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsat status = %d, want 416", resp.StatusCode)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes */1572864" {
		t.Errorf("unsat Content-Range = %q", cr)
	}

	// 8. multi-range: graceful 200 full
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Range", "bytes=0-1,5-6")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("multi-range status = %d, want 200", resp.StatusCode)
	}

	// malformed: 200 full
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Range", "bytes=garbage")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("garbage range status = %d, want 200", resp.StatusCode)
	}
}

func TestFileOfflineAndExpired(t *testing.T) {
	body := []byte{9, 8, 7}

	// offline: 503 with the honest copy
	srv, _ := newTestServer(t, body, false)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/abc123/file")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("offline file = %d, want 503", resp.StatusCode)
	}
	if !strings.Contains(string(b), "sender's device") {
		t.Errorf("offline copy: %q", b)
	}

	// expired: 410 tombstone
	reg := share.NewRegistry(time.Hour)
	reg.Register(share.Info{ID: "gone", Size: 3, Expires: time.Now().Add(-time.Second)}, nil)
	ts2 := httptest.NewServer(New(ServerConfig{Registry: reg}))
	defer ts2.Close()
	resp, _ = http.Get(ts2.URL + "/gone/file")
	if resp.StatusCode != http.StatusGone {
		t.Errorf("expired file = %d, want 410", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// page: 410 too, with noindex
	resp, _ = http.Get(ts2.URL + "/gone")
	if resp.StatusCode != http.StatusGone || resp.Header.Get("Robots") != "noindex" {
		t.Errorf("expired page: %d robots=%q", resp.StatusCode, resp.Header.Get("Robots"))
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// TestEndToEndThroughHub: the full slice — hub.Accept of a real
// daemon connection (TCP), then HTTP range reads served through it.
// This is the PRD §9 "in-process, real socket" test, one level below
// the two-real-binaries E2E in phase 8.
func TestEndToEndThroughHub(t *testing.T) {
	const size = int64(512 * 1024)
	ownerKey, shareID := protocol.NewOwnerKey()
	var body []byte
	for i := int64(0); i < size; i++ {
		body = append(body, byteAt(i))
	}

	reg := share.NewRegistry(100 * time.Millisecond)

	// daemon socket pair over real TCP
	dConn, rConn := net.Pipe()
	defer dConn.Close()
	defer rConn.Close()

	// daemon side: announce, then answer reads from body
	go func() {
		protocol.EncodeJSON(dConn, protocol.THello, protocol.Hello{Version: "0.1"})
		protocol.EncodeJSON(dConn, protocol.TSend, protocol.Send{
			ShareID: shareID, OwnerKey: ownerKey, Name: "e2e.mp4", Size: size, MIME: "video/mp4", TTLSec: 3600,
		})
		serveReads(dConn, body)
	}()

	if _, err := (&hub.Bridge{Registry: reg}).Accept(rConn); err != nil {
		t.Fatalf("hub accept: %v", err)
	}

	ts := httptest.NewServer(New(ServerConfig{Registry: reg}))
	defer ts.Close()

	// range read through the whole stack
	req, _ := http.NewRequest("GET", ts.URL+"/"+shareID+"/file", nil)
	req.Header.Set("Range", "bytes=10-1057")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || len(data) != 1048 {
		t.Fatalf("e2e range: %d len %d", resp.StatusCode, len(data))
	}
	for i := range data {
		if data[i] != byteAt(10+int64(i)) {
			t.Fatalf("e2e byte %d", 10+i)
		}
	}
}

// serveReads answers TRead frames from body until the socket dies.
func serveReads(conn io.ReadWriter, body []byte) {
	br := bufio.NewReader(conn)
	for {
		fr, err := protocol.ReadFrame(br)
		if err != nil {
			return
		}
		if fr.Type != protocol.TRead {
			continue
		}
		var rd protocol.Read
		if json.Unmarshal(fr.JSON, &rd) != nil {
			return
		}
		if rd.Offset < 0 || rd.Length < 1 || rd.Offset+rd.Length > int64(len(body)) {
			protocol.EncodeJSON(conn, protocol.TErr, protocol.Err{Code: "denied"})
			continue
		}
		buf := body[rd.Offset : rd.Offset+rd.Length]
		for off := 0; off < len(buf); off += protocol.MaxBody {
			end := off + protocol.MaxBody
			if end > len(buf) {
				end = len(buf)
			}
			if protocol.EncodeData(conn, buf[off:end]) != nil {
				return
			}
		}
	}
}
