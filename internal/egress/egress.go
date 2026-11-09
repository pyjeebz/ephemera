// Package egress is the only way out of a sandbox. A sandbox has no network
// of its own; its HTTP proxy connections reach this proxy, which lets a
// connection through only if its host is on the sandbox's allow list and
// resolves to a public address, and records every attempt.
package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
)

// Policy decides which hosts a sandbox may reach.
type Policy struct {
	// Allow lists host patterns: "example.com" (that host), "*.example.com"
	// (its subdomains), or "*" (any public host). A pattern may end in
	// ":port"; otherwise ports 80 and 443 are allowed.
	Allow []string
}

// Allows reports whether host:port may be reached, and the rule that
// matched.
func (p Policy) Allows(host string, port int) (bool, string) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pat := range p.Allow {
		ph, pp := splitPattern(pat)
		if pp == 0 {
			if port != 80 && port != 443 {
				continue
			}
		} else if pp != port {
			continue
		}
		if matchHost(ph, host) {
			return true, pat
		}
	}
	return false, ""
}

func splitPattern(pat string) (string, int) {
	pat = strings.ToLower(strings.TrimSpace(pat))
	if i := strings.LastIndexByte(pat, ':'); i > 0 {
		if n, err := strconv.Atoi(pat[i+1:]); err == nil {
			return pat[:i], n
		}
	}
	return pat, 0
}

func matchHost(pat, host string) bool {
	switch {
	case pat == "*":
		return true
	case strings.HasPrefix(pat, "*."):
		return strings.HasSuffix(host, pat[1:]) && len(host) > len(pat)-1
	default:
		return pat == host
	}
}

// ValidatePattern checks an allow-list entry.
func ValidatePattern(pat string) error {
	h, port := splitPattern(pat)
	if strings.Contains(pat, ":") && port == 0 {
		return fmt.Errorf("invalid port in %q", pat)
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("invalid port in %q", pat)
	}
	if h == "*" {
		return nil
	}
	h = strings.TrimPrefix(h, "*.")
	if h == "" || strings.ContainsAny(h, "*/@ ") || strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") {
		return fmt.Errorf("invalid host pattern %q: use host, *.domain or *", pat)
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return fmt.Errorf("%q is an IP address; allow hosts by name", pat)
	}
	return nil
}

// Proxy serves one sandbox's HTTP proxy connections.
type Proxy struct {
	Policy Policy
	// Resolver and Dial are for tests; the defaults use the system's.
	Resolver *net.Resolver
	// AllowPrivate turns off the block on private addresses, for tests.
	AllowPrivate bool

	mu   sync.Mutex
	recs []api.Connection
}

// Connections returns the records from index since on.
func (p *Proxy) Connections(since int) []api.Connection {
	p.mu.Lock()
	defer p.mu.Unlock()
	if since < 0 || since >= len(p.recs) {
		return []api.Connection{}
	}
	return append([]api.Connection(nil), p.recs[since:]...)
}

// record adds a connection to the log and returns its index, or -1 if the
// log is full. Connections are recorded when they are decided, not when
// they end, so the log never misses one that is still open.
func (p *Proxy) record(c api.Connection) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.recs) >= 10000 { // a runaway loop must not exhaust memory
		return -1
	}
	p.recs = append(p.recs, c)
	return len(p.recs) - 1
}

// update replaces a logged connection with its final figures.
func (p *Proxy) update(i int, c api.Connection) {
	if i < 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < len(p.recs) {
		p.recs[i] = c
	}
}

// Serve accepts proxy connections until ln is closed.
func (p *Proxy) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go p.handle(c)
	}
}

func (p *Proxy) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	for {
		c.SetReadDeadline(time.Now().Add(2 * time.Minute))
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		c.SetReadDeadline(time.Time{})
		if req.Method == http.MethodConnect {
			p.connect(c, br, req)
			return // the connection is now a tunnel, or was refused
		}
		if !p.forward(c, req) {
			return
		}
	}
}

func hostPort(hostport string, defPort int) (string, int, error) {
	h, ps, err := net.SplitHostPort(hostport)
	if err != nil {
		if strings.Contains(err.Error(), "missing port") {
			return hostport, defPort, nil
		}
		return "", 0, err
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port %q", ps)
	}
	return h, port, nil
}

// dial checks the policy, resolves the host, refuses non-public
// addresses, and connects.
func (p *Proxy) dial(ctx context.Context, rec *api.Connection) (net.Conn, error) {
	if _, err := netip.ParseAddr(rec.Host); err == nil {
		return nil, errors.New("connections to IP addresses are not allowed; use a host name")
	}
	ok, rule := p.Policy.Allows(rec.Host, rec.Port)
	if !ok {
		return nil, fmt.Errorf("%s:%d is not on the sandbox's allow list", rec.Host, rec.Port)
	}
	rec.Rule = rule
	r := p.Resolver
	if r == nil {
		r = net.DefaultResolver
	}
	addrs, err := r.LookupNetIP(ctx, "ip", rec.Host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", rec.Host, err)
	}
	var dialErr error
	for _, a := range addrs {
		a = a.Unmap()
		if !p.AllowPrivate && !Public(a) {
			dialErr = fmt.Errorf("%s resolves to %s, which is not a public address", rec.Host, a)
			continue
		}
		d := net.Dialer{Timeout: 10 * time.Second, Control: p.controlPublic}
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(a.String(), strconv.Itoa(rec.Port)))
		if err == nil {
			rec.IP = a.String()
			return conn, nil
		}
		dialErr = err
	}
	if dialErr == nil {
		dialErr = fmt.Errorf("no addresses for %s", rec.Host)
	}
	return nil, dialErr
}

// controlPublic re-checks the address at connect time.
func (p *Proxy) controlPublic(_, address string, _ syscall.RawConn) error {
	if p.AllowPrivate {
		return nil
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !Public(ap.Addr().Unmap()) {
		return fmt.Errorf("refusing to connect to %s", address)
	}
	return nil
}

// Public reports whether a is a globally routable unicast address: not
// loopback, private, link-local (which includes cloud metadata services),
// carrier-grade NAT (which includes Tailscale), multicast or unspecified.
func Public(a netip.Addr) bool {
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT, Tailscale
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach IPv4 private space
	netip.MustParsePrefix("2001:db8::/32"),
}

func (p *Proxy) connect(c net.Conn, br *bufio.Reader, req *http.Request) {
	start := time.Now()
	rec := api.Connection{Time: start.UTC(), Kind: "https"}
	h, port, err := hostPort(req.Host, 443)
	if err != nil {
		rec.Host, rec.Error = req.Host, err.Error()
		p.record(rec)
		io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		return
	}
	rec.Host, rec.Port = strings.ToLower(h), port
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	upstream, err := p.dial(ctx, &rec)
	cancel()
	if err != nil {
		rec.Error = err.Error()
		p.record(rec)
		msg := "ephemera: " + err.Error() + "\n"
		fmt.Fprintf(c, "HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(msg), msg)
		return
	}
	defer upstream.Close()
	rec.Allowed = true
	idx := p.record(rec)
	io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
	// Bytes the client sent after the CONNECT request.
	if n := br.Buffered(); n > 0 {
		b, _ := br.Peek(n)
		upstream.Write(b)
	}
	up, down := pipe(c, upstream)
	rec.BytesOut, rec.BytesIn = up+int64(br.Buffered()), down
	rec.DurationMS = time.Since(start).Milliseconds()
	p.update(idx, rec)
}

func pipe(client, upstream net.Conn) (up, down int64) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		up, _ = io.Copy(upstream, client)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	down, _ = io.Copy(client, upstream)
	client.Close()
	upstream.Close()
	wg.Wait()
	return up, down
}

// forward handles a plain-HTTP proxy request (an absolute URL). It returns
// false when the client connection should close.
func (p *Proxy) forward(c net.Conn, req *http.Request) bool {
	start := time.Now()
	rec := api.Connection{Time: start.UTC(), Kind: "http"}
	fail := func(code int, msg string) bool {
		rec.Error = msg
		p.record(rec)
		body := "ephemera: " + msg + "\n"
		fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			code, http.StatusText(code), len(body), body)
		return false
	}
	if req.URL == nil || req.URL.Scheme != "http" || req.URL.Host == "" {
		rec.Host = req.Host
		return fail(http.StatusBadRequest, "only http:// URLs can be proxied without CONNECT")
	}
	h, port, err := hostPort(req.URL.Host, 80)
	if err != nil {
		rec.Host = req.URL.Host
		return fail(http.StatusBadRequest, err.Error())
	}
	rec.Host, rec.Port = strings.ToLower(h), port
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	upstream, err := p.dial(ctx, &rec)
	cancel()
	if err != nil {
		return fail(http.StatusForbidden, err.Error())
	}
	defer upstream.Close()
	rec.Allowed = true
	idx := p.record(rec)
	req.RequestURI = ""
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authorization")
	req.Header.Set("Connection", "close")
	if err := req.Write(upstream); err != nil {
		rec.Error = err.Error()
		p.update(idx, rec)
		return false
	}
	resp, err := http.ReadResponse(bufio.NewReader(upstream), req)
	if err != nil {
		rec.Error = err.Error()
		p.update(idx, rec)
		return false
	}
	defer resp.Body.Close()
	resp.Close = true
	cw := &countingWriter{w: c}
	resp.Write(cw)
	rec.BytesIn = cw.n
	rec.DurationMS = time.Since(start).Milliseconds()
	p.update(idx, rec)
	return false
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
