package relay

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/share"
	"ephemera.run/ephemera/internal/upload"
)

// countingHandle is a live sender that counts the reads it answers.
type countingHandle struct {
	memHandle
	reads atomic.Int64
}

func (c *countingHandle) IssueRead(off, length int64) ([]byte, error) {
	c.reads.Add(1)
	return c.memHandle.IssueRead(off, length)
}

func patterned(size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// uploadServer registers an upload share with stored bytes of body
// already on the relay's disk.
func uploadServer(t *testing.T, body []byte, stored int, h share.Handle) (*httptest.Server, *share.Registry, *upload.Store) {
	t.Helper()
	st, err := upload.Open(t.TempDir(), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	reg := share.NewRegistry(time.Hour)
	m := upload.Meta{ID: "up", Name: "v.mp4", Size: int64(len(body)), MIME: "video/mp4", Expires: time.Now().Add(time.Hour)}
	if _, err := st.Reserve(m); err != nil {
		t.Fatal(err)
	}
	st.Put("up", 0, body[:stored])
	reg.Register(share.Info{ID: m.ID, Name: m.Name, Size: m.Size, MIME: m.MIME, Expires: m.Expires, Upload: true}, h)
	ts := httptest.NewServer(New(ServerConfig{Registry: reg, Uploads: st, ChunkSize: 4 << 10}))
	t.Cleanup(ts.Close)
	return ts, reg, st
}

func get(t *testing.T, url, rng string) (int, []byte, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, resp.Header
}

// TestStoredBytesSpareTheSender: with the sender online, ranges the
// relay already holds come off its disk; only the rest reach the
// sender.
func TestStoredBytesSpareTheSender(t *testing.T) {
	body := patterned(64 << 10)
	h := &countingHandle{memHandle: memHandle{data: body}}
	ts, _, _ := uploadServer(t, body, 32<<10, h)

	code, data, hdr := get(t, ts.URL+"/up/file", "bytes=100-20099")
	if code != 206 || !bytes.Equal(data, body[100:20100]) || hdr.Get("Content-Range") != fmt.Sprintf("bytes 100-20099/%d", len(body)) {
		t.Fatalf("stored range: %d, %d bytes, %q", code, len(data), hdr.Get("Content-Range"))
	}
	if n := h.reads.Load(); n != 0 {
		t.Fatalf("sender answered %d reads for bytes the relay holds", n)
	}

	code, data, _ = get(t, ts.URL+"/up/file", "bytes=40000-45000")
	if code != 206 || !bytes.Equal(data, body[40000:45001]) {
		t.Fatalf("live range: %d, %d bytes", code, len(data))
	}
	if h.reads.Load() == 0 {
		t.Fatal("bytes past the upload didn't come from the sender")
	}

	// how a browser opens a video: the stored head off disk, then the
	// rest live, in one response
	h.reads.Store(0)
	code, data, _ = get(t, ts.URL+"/up/file", "bytes=0-")
	if code != 206 || !bytes.Equal(data, body) {
		t.Fatalf("open-ended range: %d, %d bytes", code, len(data))
	}
	if n := h.reads.Load(); n != 8 { // 32 KiB live in 4 KiB chunks
		t.Fatalf("sender answered %d reads, want 8 for the part not uploaded", n)
	}

	var st statusBody
	resp, _ := http.Get(ts.URL + "/up/status")
	readJSON(t, resp, &st)
	if st.State != "live" || !st.Upload || st.Uploaded != 32<<10 {
		t.Fatalf("status mid-upload = %+v", st)
	}
}

// TestFinishedUploadIsAvailableOffline: once the whole file is on the
// relay, the link reads "available" and serves everything with the
// sender gone.
func TestFinishedUploadIsAvailableOffline(t *testing.T) {
	body := patterned(50 << 10)
	ts, _, _ := uploadServer(t, body, len(body), nil)

	var st statusBody
	resp, _ := http.Get(ts.URL + "/up/status")
	readJSON(t, resp, &st)
	if st.State != "available" || st.Uploaded != int64(len(body)) {
		t.Fatalf("status = %+v, want available", st)
	}
	code, data, _ := get(t, ts.URL+"/up/file", "")
	if code != 200 || !bytes.Equal(data, body) {
		t.Fatalf("full body: %d, %d bytes", code, len(data))
	}
	code, data, _ = get(t, ts.URL+"/up/file", "bytes=-100")
	if code != 206 || !bytes.Equal(data, body[len(body)-100:]) {
		t.Fatalf("suffix range: %d, %d bytes", code, len(data))
	}
}

// TestPartialUploadOffline: with the sender gone mid-upload, the
// stored head plays and a request past it ends short.
func TestPartialUploadOffline(t *testing.T) {
	body := patterned(50 << 10)
	ts, _, _ := uploadServer(t, body, 20<<10, nil)

	var st statusBody
	resp, _ := http.Get(ts.URL + "/up/status")
	readJSON(t, resp, &st)
	if st.State != "offline" || st.Prefix != 20<<10 {
		t.Fatalf("status = %+v, want offline with a 20 KiB prefix", st)
	}
	code, data, hdr := get(t, ts.URL+"/up/file", "bytes=10000-30000")
	if code != 206 || !bytes.Equal(data, body[10000:20<<10]) || hdr.Get("Content-Range") != fmt.Sprintf("bytes 10000-%d/%d", 20<<10-1, len(body)) {
		t.Fatalf("straddling range: %d, %d bytes, %q", code, len(data), hdr.Get("Content-Range"))
	}
	if code, _, _ := get(t, ts.URL+"/up/file", "bytes=30000-"); code != http.StatusServiceUnavailable {
		t.Fatalf("range past the upload = %d, want 503", code)
	}
}

// TestRestoreUploadsAfterRestart: a fresh share table gets the stored
// shares back, so a finished upload survives a relay restart.
func TestRestoreUploadsAfterRestart(t *testing.T) {
	body := patterned(8 << 10)
	_, _, st := uploadServer(t, body, len(body), nil)

	reg := share.NewRegistry(time.Hour)
	if n := RestoreUploads(reg, st); n != 1 {
		t.Fatalf("restored %d shares, want 1", n)
	}
	ts := httptest.NewServer(New(ServerConfig{Registry: reg, Uploads: st}))
	defer ts.Close()
	code, data, _ := get(t, ts.URL+"/up/file", "")
	if code != 200 || !bytes.Equal(data, body) {
		t.Fatalf("after restart: %d, %d bytes", code, len(data))
	}
}

// TestHeadAsksNothing: HEAD gets the size and headers without the
// relay reading the file, from its disk or from the sender.
func TestHeadAsksNothing(t *testing.T) {
	body := patterned(64 << 10)
	h := &countingHandle{memHandle: memHandle{data: body}}
	ts, _, _ := uploadServer(t, body, 0, h)
	resp, err := http.Head(ts.URL + "/up/file")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength != int64(len(body)) || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("HEAD = %d, length %d, ranges %q", resp.StatusCode, resp.ContentLength, resp.Header.Get("Accept-Ranges"))
	}
	if n := h.reads.Load(); n != 0 {
		t.Fatalf("HEAD made the sender answer %d reads", n)
	}
}
