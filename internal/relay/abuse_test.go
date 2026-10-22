package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/metrics"
	"ephemera.run/ephemera/internal/ratelimit"
	"ephemera.run/ephemera/internal/share"
)

func TestRateLimit429(t *testing.T) {
	body := []byte{1, 2, 3}
	reg := share.NewRegistry(time.Hour)
	reg.Register(share.Info{ID: "x", Name: "v.mp4", Size: 3, MIME: "video/mp4",
		Expires: time.Now().Add(time.Hour)}, &memHandle{data: body})

	lim := ratelimit.New(1.0/60, 3) // burst 3; no refill within the test
	srv := New(ServerConfig{Registry: reg, Limiter: lim})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// burst: 3 requests pass
	for i := 0; i < 3; i++ {
		resp, err := http.Get(ts.URL + "/x/status")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("req %d: status %d, want 200", i, resp.StatusCode)
		}
		if resp.Header.Get("X-RateLimit-Remaining") == "" {
			t.Error("missing X-RateLimit-Remaining")
		}
	}
	// 4th: 429 with Retry-After
	resp, err := http.Get(ts.URL + "/x/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("missing Retry-After on 429")
	}
	if metrics.RateLimited429.Value() == 0 {
		t.Error("rate_limited_429 counter did not move")
	}
}

func TestAdminAuthAndKill(t *testing.T) {
	body := []byte{1, 2, 3}
	reg := share.NewRegistry(time.Hour)
	reg.Register(share.Info{ID: "x", Name: "v.mp4", Size: 3, MIME: "video/mp4",
		Expires: time.Now().Add(time.Hour)}, &memHandle{data: body})

	srv := New(ServerConfig{Registry: reg, AdminToken: "sekrit"})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// no token: 401
	resp, _ := http.Get(ts.URL + "/admin/metrics")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("metrics without token: %d", resp.StatusCode)
	}

	// wrong token: 401
	req, _ := http.NewRequest("GET", ts.URL+"/admin/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("metrics wrong token: %d", resp.StatusCode)
	}

	// right token: counters render
	req, _ = http.NewRequest("GET", ts.URL+"/admin/metrics", nil)
	req.Header.Set("Authorization", "Bearer sekrit")
	resp, _ = http.DefaultClient.Do(req)
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if _, ok := m["requests"]; !ok {
		t.Errorf("metrics snapshot missing requests: %v", m)
	}

	// disabled admin: 404
	srv2 := New(ServerConfig{Registry: share.NewRegistry(time.Hour)})
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()
	resp, _ = http.Get(ts2.URL + "/admin/metrics")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled admin: %d, want 404", resp.StatusCode)
	}

	// kill: share gone
	req, _ = http.NewRequest("POST", ts.URL+"/admin/kill", strings.NewReader(`{"id":"x"}`))
	req.Header.Set("Authorization", "Bearer sekrit")
	req.Header.Set("Content-Type", "application/json")
	resp, _ = http.DefaultClient.Do(req)
	var killResp struct{ Killed bool }
	json.NewDecoder(resp.Body).Decode(&killResp)
	resp.Body.Close()
	if !killResp.Killed {
		t.Fatal("kill returned killed=false")
	}
	// lookup now 404s
	resp, _ = http.Get(ts.URL + "/x/status")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("killed share status: %d, want 404", resp.StatusCode)
	}

	// kill unknown: 404
	req, _ = http.NewRequest("POST", ts.URL+"/admin/kill", strings.NewReader(`{"id":"ghost"}`))
	req.Header.Set("Authorization", "Bearer sekrit")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("kill unknown: %d, want 404", resp.StatusCode)
	}
}

func TestCountersMove(t *testing.T) {
	body := make([]byte, 4096)
	for i := range body {
		body[i] = byte(i % 251)
	}
	reg := share.NewRegistry(time.Hour)
	reg.Register(share.Info{ID: "y", Name: "v.mp4", Size: 4096, MIME: "video/mp4",
		Expires: time.Now().Add(time.Hour)}, &memHandle{data: body})

	before := metrics.BytesServed.Value()
	rangeBefore := metrics.RangeRequests.Value()

	srv := New(ServerConfig{Registry: reg, ChunkSize: 1024})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/y/file", nil)
	req.Header.Set("Range", "bytes=0-2047")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := metrics.BytesServed.Value() - before; got != 2048 {
		t.Errorf("bytes_served delta = %d, want 2048", got)
	}
	if got := metrics.RangeRequests.Value() - rangeBefore; got != 1 {
		t.Errorf("range_requests delta = %d, want 1", got)
	}
}
