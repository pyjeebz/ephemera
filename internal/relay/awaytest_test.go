package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/away"
	"ephemera.run/ephemera/internal/metrics"
	"ephemera.run/ephemera/internal/share"
)

func TestAwayPrefixServesOffline(t *testing.T) {
	const size = int64(256 << 10)
	const prefixLen = int64(128 << 10)
	var body []byte
	for i := int64(0); i < size; i++ {
		body = append(body, byte(i%251))
	}

	reg := share.NewRegistry(time.Hour)
	h := &memHandle{data: body}
	reg.Register(share.Info{
		ID: "aw", Name: "v.mp4", Size: size, MIME: "video/mp4",
		Expires: time.Now().Add(time.Hour), AwayPrefix: prefixLen,
	}, h)

	ast := away.New(1 << 20)
	// daemon trickled the prefix already
	for off := int64(0); off < prefixLen; off += 16 << 10 {
		n := min(16<<10, prefixLen-off)
		ast.Put("aw", off, body[off:off+n])
	}

	srv := New(ServerConfig{Registry: reg, Away: ast})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// live read works regardless
	resp := mustStatus(t, ts.URL, "aw", "")
	if resp != 200 {
		t.Fatalf("live read = %d", resp)
	}

	// sender vanishes
	reg.Detach("aw", h)

	// status reports prefix
	resp2, _ := http.Get(ts.URL + "/aw/status")
	var st statusBody
	readJSON(t, resp2, &st)
	if st.State != "offline" || st.Prefix != prefixLen {
		t.Fatalf("status = %+v, want offline prefix=%d", st, prefixLen)
	}

	// prefix read: 206 from the blob, byte-exact
	req, _ := http.NewRequest("GET", ts.URL+"/aw/file", nil)
	req.Header.Set("Range", "bytes=1000-11000")
	resp3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusPartialContent || len(data) != 10001 {
		t.Fatalf("prefix read = %d len %d", resp3.StatusCode, len(data))
	}
	for i := range data {
		if data[i] != byte((1000+int64(i))%251) {
			t.Fatalf("prefix byte %d wrong", 1000+i)
		}
	}

	// straddling read: serves the covered part, counts an outlast
	outlastsBefore := metrics.AwayOutlasts.Value()
	req, _ = http.NewRequest("GET", ts.URL+"/aw/file", nil)
	req.Header.Set("Range", "bytes=131000-140000")
	resp4, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp4.Body)
	resp4.Body.Close()
	// covered = 131072-131000 = 72 bytes; short body is honest
	if len(data) != 72 {
		t.Fatalf("straddle len = %d, want 72", len(data))
	}
	if metrics.AwayOutlasts.Value() != outlastsBefore+1 {
		t.Error("outlast not counted")
	}

	// fully past the prefix: 503
	req, _ = http.NewRequest("GET", ts.URL+"/aw/file", nil)
	req.Header.Set("Range", "bytes=200000-")
	resp5, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp5.Body.Close()
	if resp5.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("past-prefix read = %d, want 503", resp5.StatusCode)
	}

	// served bytes counted from the away store
	if metrics.AwayBytesServed.Value() == 0 {
		t.Error("away_bytes_served did not move")
	}
}

func TestAwayOptOutAndDisabled(t *testing.T) {
	const size = int64(64 << 10)
	body := make([]byte, size)

	// no away opt-in: status never reports a prefix
	reg := share.NewRegistry(time.Hour)
	h := &memHandle{data: body}
	reg.Register(share.Info{
		ID: "nc", Name: "v.mp4", Size: size, MIME: "video/mp4",
		Expires: time.Now().Add(time.Hour), // AwayPrefix zero
	}, h)
	ast := away.New(1 << 20)
	srv := New(ServerConfig{Registry: reg, Away: ast})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	reg.Detach("nc", h)
	resp, _ := http.Get(ts.URL + "/nc/status")
	var st statusBody
	readJSON(t, resp, &st)
	if st.Prefix != 0 {
		t.Errorf("prefix reported for non-away share: %d", st.Prefix)
	}
	resp2, _ := http.Get(ts.URL + "/nc/file")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("non-away offline read = %d", resp2.StatusCode)
	}
}

func TestAwayDropOnExpiry(t *testing.T) {
	const size = int64(64 << 10)
	body := make([]byte, size)
	reg := share.NewRegistry(time.Hour)
	h := &memHandle{data: body}
	reg.Register(share.Info{
		ID: "gone", Name: "v.mp4", Size: size, MIME: "video/mp4",
		Expires: time.Now().Add(time.Hour), AwayPrefix: size,
	}, h)

	ast := away.New(1 << 20)
	reg.OnExpire(ast.DropShare)
	ast.Put("gone", 0, body)
	if ast.Held() == 0 {
		t.Fatal("setup failed")
	}
	reg.Kill("gone")
	if ast.Held() != 0 {
		t.Errorf("blob survived kill: %d", ast.Held())
	}
}

func mustStatus(t *testing.T, base, id, path string) int {
	t.Helper()
	resp, err := http.Get(base + "/" + id + "/file")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}
