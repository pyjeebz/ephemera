package relay

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"ephemera.run/ephemera/internal/ratelimit"
)

func TestClientIP(t *testing.T) {
	proxies := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}
	for name, tc := range map[string]struct {
		trusted []netip.Prefix
		peer    string
		xff     []string
		want    string
	}{
		"no proxies, header ignored":  {nil, "203.0.113.7:5000", []string{"1.2.3.4"}, "203.0.113.7"},
		"untrusted peer, ignored":     {proxies, "203.0.113.7:5000", []string{"1.2.3.4"}, "203.0.113.7"},
		"trusted proxy":               {proxies, "10.0.0.2:5000", []string{"1.2.3.4"}, "1.2.3.4"},
		"client spoofs the left":      {proxies, "10.0.0.2:5000", []string{"6.6.6.6, 1.2.3.4"}, "1.2.3.4"},
		"chain of proxies":            {proxies, "10.0.0.2:5000", []string{"1.2.3.4, 10.0.0.1"}, "1.2.3.4"},
		"split across header lines":   {proxies, "10.0.0.2:5000", []string{"6.6.6.6", "1.2.3.4"}, "1.2.3.4"},
		"all hops trusted":            {proxies, "10.0.0.2:5000", []string{"10.0.0.9"}, "10.0.0.9"},
		"trusted proxy, no header":    {proxies, "10.0.0.2:5000", nil, "10.0.0.2"},
		"junk stops at the proxy":     {proxies, "10.0.0.2:5000", []string{"1.2.3.4, junk"}, "10.0.0.2"},
		"ipv6 proxy and client":       {proxies, "[::1]:5000", []string{"2001:db8::1"}, "2001:db8::1"},
		"ipv4-mapped client":          {proxies, "10.0.0.2:5000", []string{"::ffff:1.2.3.4"}, "1.2.3.4"},
		"ipv4-mapped peer is trusted": {proxies, "[::ffff:10.0.0.2]:5000", []string{"1.2.3.4"}, "1.2.3.4"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		for _, v := range tc.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		s := &Server{cfg: ServerConfig{TrustedProxies: tc.trusted}}
		if got := s.clientIP(r); got != tc.want {
			t.Errorf("%s: clientIP = %q, want %q", name, got, tc.want)
		}
	}
}

// TestRateLimitBehindProxy: two clients behind one trusted proxy get
// their own buckets; without the proxy trusted, they share its one.
func TestRateLimitBehindProxy(t *testing.T) {
	get := func(ts *httptest.Server, client string) int {
		req, _ := http.NewRequest("GET", ts.URL+"/", nil)
		req.Header.Set("X-Forwarded-For", client)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	serve := func(trusted []netip.Prefix) *httptest.Server {
		return httptest.NewServer(New(ServerConfig{
			Limiter:        ratelimit.New(1.0/60, 1), // one request, then none within the test
			TrustedProxies: trusted,
		}))
	}

	// the test server's peer is 127.0.0.1: the "proxy"
	ts := serve([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	defer ts.Close()
	if code := get(ts, "1.2.3.4"); code != http.StatusOK {
		t.Fatalf("client A first request: %d, want 200", code)
	}
	if code := get(ts, "1.2.3.4"); code != http.StatusTooManyRequests {
		t.Fatalf("client A second request: %d, want 429", code)
	}
	if code := get(ts, "5.6.7.8"); code != http.StatusOK {
		t.Fatalf("client B behind the same proxy: %d, want 200 (own bucket)", code)
	}

	untrusted := serve(nil)
	defer untrusted.Close()
	get(untrusted, "1.2.3.4")
	if code := get(untrusted, "5.6.7.8"); code != http.StatusTooManyRequests {
		t.Fatalf("untrusted proxy: client B got %d, want 429 (header ignored, one bucket)", code)
	}
}
