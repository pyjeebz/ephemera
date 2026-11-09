package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
)

func TestPolicy(t *testing.T) {
	p := Policy{Allow: []string{"registry.npmjs.org", "*.github.com", "db.example.com:5432"}}
	cases := []struct {
		host string
		port int
		want bool
	}{
		{"registry.npmjs.org", 443, true},
		{"REGISTRY.npmjs.org.", 443, true},
		{"registry.npmjs.org", 80, true},
		{"registry.npmjs.org", 22, false},
		{"evil-registry.npmjs.org", 443, false},
		{"api.github.com", 443, true},
		{"github.com", 443, false}, // *.github.com is subdomains only
		{"notgithub.com", 443, false},
		{"db.example.com", 5432, true},
		{"db.example.com", 443, false},
		{"example.com", 443, false},
	}
	for _, c := range cases {
		if got, _ := p.Allows(c.host, c.port); got != c.want {
			t.Errorf("Allows(%s, %d) = %v, want %v", c.host, c.port, got, c.want)
		}
	}
	if ok, _ := (Policy{Allow: []string{"*"}}).Allows("anything.example", 443); !ok {
		t.Error(`"*" should allow any host`)
	}
	if ok, _ := (Policy{}).Allows("example.com", 443); ok {
		t.Error("an empty policy must allow nothing")
	}
}

func TestValidatePattern(t *testing.T) {
	for _, ok := range []string{"example.com", "*.example.com", "*", "example.com:8443"} {
		if err := ValidatePattern(ok); err != nil {
			t.Errorf("ValidatePattern(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "*.", "a*.example.com", "1.2.3.4", "http://x.com", "x.com:abc", ".x.com", "x.com:99999"} {
		if ValidatePattern(bad) == nil {
			t.Errorf("ValidatePattern(%q): want error", bad)
		}
	}
}

func TestPublic(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !Public(netip.MustParseAddr(s)) {
			t.Errorf("%s should be public", s)
		}
	}
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.111.12.48", "0.0.0.0", "::1", "fe80::1", "fd00::1", "224.0.0.1", "64:ff9b::a00:1"} {
		if Public(netip.MustParseAddr(s)) {
			t.Errorf("%s must not count as public", s)
		}
	}
}

// testProxy runs a proxy whose resolver maps every name to 127.0.0.1, with
// the private-address block off, so tests can reach local servers by name.
func testProxy(t *testing.T, allow ...string) (*Proxy, string) {
	t.Helper()
	p := &Proxy{Policy: Policy{Allow: allow}, AllowPrivate: true, Resolver: localResolver()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go p.Serve(ln)
	return p, ln.Addr().String()
}

// localResolver answers every query with 127.0.0.1 via a tiny DNS stub.
func localResolver() *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return fakeDNS(), nil
	}}
}

func TestConnectAllowedAndBlocked(t *testing.T) {
	// An upstream TCP server standing in for a remote HTTPS service.
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() {
				line, _ := bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, "upstream got "+line)
				c.Close()
			}()
		}
	}()
	upPort := up.Addr().(*net.TCPAddr).Port

	p, addr := testProxy(t, fmt.Sprintf("allowed.test:%d", upPort))

	tunnel := func(target string) (string, string) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			return resp.Status, string(b)
		}
		io.WriteString(c, "hello\n")
		reply, _ := br.ReadString('\n')
		return resp.Status, reply
	}

	status, reply := tunnel(fmt.Sprintf("allowed.test:%d", upPort))
	if !strings.HasPrefix(status, "200") || reply != "upstream got hello\n" {
		t.Errorf("allowed: %s %q", status, reply)
	}
	status, body := tunnel(fmt.Sprintf("blocked.test:%d", upPort))
	if !strings.HasPrefix(status, "403") || !strings.Contains(body, "not on the sandbox's allow list") {
		t.Errorf("blocked: %s %q", status, body)
	}
	status, _ = tunnel(fmt.Sprintf("127.0.0.1:%d", upPort))
	if !strings.HasPrefix(status, "403") {
		t.Errorf("IP address: %s, want 403", status)
	}

	// The tunnel's byte counts are filled in once the proxy has finished
	// with it, a moment after the client closes.
	var recs []api.Connection
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		recs = p.Connections(0)
		if len(recs) == 3 && recs[0].BytesIn > 0 || time.Now().After(deadline) {
			break
		}
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3: %+v", len(recs), recs)
	}
	if !recs[0].Allowed || recs[0].Host != "allowed.test" || recs[0].BytesIn == 0 {
		t.Errorf("record 0: %+v", recs[0])
	}
	if recs[1].Allowed || recs[1].Host != "blocked.test" || recs[1].Error == "" {
		t.Errorf("record 1: %+v", recs[1])
	}
	if got := p.Connections(2); len(got) != 1 || got[0].Host != "127.0.0.1" {
		t.Errorf("Connections(2) = %+v", got)
	}
}

func TestPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "served %s", r.URL.Path)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	p, addr := testProxy(t, fmt.Sprintf("plain.test:%d", port))

	proxyURL, _ := url.Parse("http://" + addr)
	hc := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := hc.Get(fmt.Sprintf("http://plain.test:%d/pkg", port))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "served /pkg" {
		t.Errorf("body = %q", b)
	}
	resp, err = hc.Get(fmt.Sprintf("http://other.test:%d/", port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("blocked host: status %d", resp.StatusCode)
	}
	if recs := p.Connections(0); len(recs) != 2 || !recs[0].Allowed || recs[1].Allowed {
		t.Errorf("records: %+v", recs)
	}
}

func TestPrivateAddressesBlockedEvenWhenOpen(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	p := &Proxy{Policy: Policy{Allow: []string{"*:" + fmt.Sprint(up.Addr().(*net.TCPAddr).Port)}}, Resolver: localResolver()}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go p.Serve(ln)

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	target := fmt.Sprintf("metadata.internal:%d", up.Addr().(*net.TCPAddr).Port)
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 403 || !strings.Contains(string(b), "not a public address") {
		t.Errorf("status %d body %q; want 403 for a name resolving to 127.0.0.1", resp.StatusCode, b)
	}
}

// An open tunnel is in the log while it is still open, so a log read right
// after a command exits never misses it.
func TestOpenConnectionIsLogged(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	held := make(chan net.Conn, 1)
	go func() {
		c, err := up.Accept()
		if err == nil {
			held <- c
		}
	}()
	port := up.Addr().(*net.TCPAddr).Port
	p, addr := testProxy(t, fmt.Sprintf("slow.test:%d", port))
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT slow.test:%d HTTP/1.1\r\nHost: slow.test\r\n\r\n", port)
	if resp, err := http.ReadResponse(bufio.NewReader(c), nil); err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	recs := p.Connections(0)
	if len(recs) != 1 || !recs[0].Allowed || recs[0].Host != "slow.test" {
		t.Errorf("open tunnel not logged: %+v", recs)
	}
	(<-held).Close()
}
