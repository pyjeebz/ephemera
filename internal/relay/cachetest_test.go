package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/cache"
	"ephemera.run/ephemera/internal/metrics"
	"ephemera.run/ephemera/internal/share"
)

func TestCacheWriteThroughAndReplay(t *testing.T) {
	const size = int64(256 << 10)
	var body []byte
	for i := int64(0); i < size; i++ {
		body = append(body, byte(i%251))
	}
	reg := share.NewRegistry(time.Hour)
	h := &memHandle{data: body}
	reg.Register(share.Info{
		ID: "c", Name: "v.mp4", Size: size, MIME: "video/mp4",
		Expires: time.Now().Add(time.Hour), Cacheable: true,
	}, h)

	st := cache.New(1 << 20)
	srv := New(ServerConfig{Registry: reg, Cache: st, ChunkSize: 64 << 10})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// read the first 128KB while live: fills the cache
	req, _ := http.NewRequest("GET", ts.URL+"/c/file", nil)
	req.Header.Set("Range", "bytes=0-131071")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(data) != 131072 {
		t.Fatalf("live read len = %d", len(data))
	}
	if st.Held() != 131072 {
		t.Errorf("cache held %d, want 131072", st.Held())
	}

	// sender vanishes
	reg.Detach("c", h)

	// status: offline but replayable
	resp, _ = http.Get(ts.URL + "/c/status")
	var stt statusBody
	readJSON(t, resp, &stt)
	if stt.State != "offline" {
		t.Fatalf("state = %q", stt.State)
	}
	if !stt.Replay {
		t.Error("status does not report replay")
	}

	// replay read of cached bytes: 206 from RAM
	req, _ = http.NewRequest("GET", ts.URL+"/c/file", nil)
	req.Header.Set("Range", "bytes=0-65535")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("replay status = %d", resp.StatusCode)
	}
	if len(data) != 65536 {
		t.Fatalf("replay len = %d", len(data))
	}
	for i := range data {
		if data[i] != byte(i%251) {
			t.Fatalf("replay byte %d wrong", i)
		}
	}

	// uncached range: honest 503
	req, _ = http.NewRequest("GET", ts.URL+"/c/file", nil)
	req.Header.Set("Range", "bytes=200000-200999")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("uncached offline read = %d, want 503", resp.StatusCode)
	}

	// replay counter moved
	if metrics.BytesCachedReplay.Value() < 65536 {
		t.Error("bytes_cached_replay did not move")
	}
}

func TestCacheOptOut(t *testing.T) {
	const size = int64(64 << 10)
	body := make([]byte, size)
	reg := share.NewRegistry(time.Hour)
	h := &memHandle{data: body}
	reg.Register(share.Info{
		ID: "nc", Name: "v.mp4", Size: size, MIME: "video/mp4",
		Expires: time.Now().Add(time.Hour), Cacheable: false, // opted out
	}, h)

	st := cache.New(1 << 20)
	srv := New(ServerConfig{Registry: reg, Cache: st})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// read while live
	resp, _ := http.Get(ts.URL + "/nc/file")
	resp.Body.Close()

	reg.Detach("nc", h)

	if st.Held() != 0 {
		t.Errorf("opt-out share cached %d bytes", st.Held())
	}
	resp, _ = http.Get(ts.URL + "/nc/status")
	var stt statusBody
	readJSON(t, resp, &stt)
	if stt.Replay {
		t.Error("replay reported for opted-out share")
	}
	resp, _ = http.Get(ts.URL + "/nc/file")
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("opted-out offline read = %d", resp.StatusCode)
	}
}

func TestCacheDroppedOnExpiry(t *testing.T) {
	const size = int64(64 << 10)
	body := make([]byte, size)
	reg := share.NewRegistry(time.Hour)
	h := &memHandle{data: body}
	reg.Register(share.Info{
		ID: "gone", Name: "v.mp4", Size: size, MIME: "video/mp4",
		Expires: time.Now().Add(time.Hour), Cacheable: true,
	}, h)

	st := cache.New(1 << 20)
	New(ServerConfig{Registry: reg, Cache: st}) // wiring registers the hook

	reg.OnExpire(func(id string) { st.DropShare(id) })

	// fill, then kill: hook must drop bytes
	st.Put("gone", 0, body)
	if st.Held() == 0 {
		t.Fatal("setup: put failed")
	}
	reg.Kill("gone")
	if st.Held() != 0 {
		t.Errorf("bytes survived kill: %d", st.Held())
	}
}

func readJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}
