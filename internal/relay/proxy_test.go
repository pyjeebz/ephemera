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

func TestClientIPHeader(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	for name, tc := range map[string]struct {
		trusted []netip.Prefix
		peer    string
		header  string
		want    string
	}{
		"no proxies listed, header read":  {nil, "172.16.0.5:5000", "1.2.3.4", "1.2.3.4"},
		"from a trusted proxy":            {proxies, "10.0.0.2:5000", "1.2.3.4", "1.2.3.4"},
		"from anyone else, ignored":       {proxies, "203.0.113.7:5000", "1.2.3.4", "203.0.113.7"},
		"wins over x-forwarded-for":       {proxies, "10.0.0.2:5000", "2001:db8::1", "2001:db8::1"},
		"ipv4-mapped":                     {nil, "172.16.0.5:5000", "::ffff:1.2.3.4", "1.2.3.4"},
		"missing, falls back to the peer": {nil, "172.16.0.5:5000", "", "172.16.0.5"},
		"junk, falls back to the peer":    {nil, "172.16.0.5:5000", "junk", "172.16.0.5"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", "6.6.6.6")
		if tc.header != "" {
			r.Header.Set("Fly-Client-IP", tc.header)
		}
		s := &Server{cfg: ServerConfig{TrustedProxies: tc.trusted, ClientIPHeader: "Fly-Client-IP"}}
		if got := s.clientIP(r); got != tc.want {
			t.Errorf("%s: clientIP = %q, want %q", name, got, tc.want)
		}
	}
}

func TestRateKey(t *testing.T) {
	for peer, want := range map[string]string{
		"203.0.113.7:5000":            "203.0.113.7",
		"[2001:db8:1:2:aaaa::1]:5000": "2001:db8:1:2::/64",
		"[2001:db8:1:2:ffff::9]:5000": "2001:db8:1:2::/64",
		"[2001:db8:1:3::1]:5000":      "2001:db8:1:3::/64",
		"[::ffff:203.0.113.7]:5000":   "203.0.113.7",
		"[fe80::1%eth0]:5000":         "fe80::/64",
		"not-an-address":              "not-an-address",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = peer
		if got := (&Server{}).rateKey(r); got != want {
			t.Errorf("rateKey(%s) = %q, want %q", peer, got, want)
		}
	}
}

// TestRateLimitIPv6Prefix: addresses in one /64 share a bucket, so a
// client can't dodge the limit by rotating its interface id.
func TestRateLimitIPv6Prefix(t *testing.T) {
	ts := httptest.NewServer(New(ServerConfig{
		Limiter:        ratelimit.New(1.0/60, 1), // one request, then none within the test
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	}))
	defer ts.Close()
	get := func(client string) int {
		req, _ := http.NewRequest("GET", ts.URL+"/", nil)
		req.Header.Set("X-Forwarded-For", client)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("2001:db8:1:2::1"); code != http.StatusOK {
		t.Fatalf("first request: %d, want 200", code)
	}
	if code := get("2001:db8:1:2::2"); code != http.StatusTooManyRequests {
		t.Fatalf("same /64, new address: %d, want 429", code)
	}
	if code := get("2001:db8:1:3::1"); code != http.StatusOK {
		t.Fatalf("next /64: %d, want 200 (own bucket)", code)
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
