package cli

import (
	"cmp"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"

	"ephemera.run/ephemera/internal/abuse"
	"ephemera.run/ephemera/internal/auth"
	awaystore "ephemera.run/ephemera/internal/away"
	cachestore "ephemera.run/ephemera/internal/cache"
	"ephemera.run/ephemera/internal/daemon"
	"ephemera.run/ephemera/internal/hub"
	"ephemera.run/ephemera/internal/ratelimit"
	"ephemera.run/ephemera/internal/relay"
	"ephemera.run/ephemera/internal/share"
	"ephemera.run/ephemera/internal/wsconn"
)

// serveFlags holds the flags of the relay serve subcommand.
type serveFlags struct {
	addr       string
	tcp        string // optional raw TCP bridge listener (host:port)
	adminToken string
	cacheMB    int64
	awayMB     int64
	maxTTL     int64 // seconds
	maxSizeMB  int64
	tokens     string // sender tokens file
	open       bool   // no sender auth at all
	tlsCert    string // PEM certificate chain; with tlsKey, serve HTTPS
	tlsKey     string
	dataDir    string // operator state: reports, suspensions
	publicURL  string // base of the links receivers open
	proxies    string // trusted reverse proxies, comma-separated
}

// runServe is the relay binary's main: receiver-facing HTTP surface
// plus the daemon WS bridge at /daemon.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f serveFlags
	fs.StringVar(&f.addr, "addr", ":7100", "HTTP listen address")
	fs.StringVar(&f.tcp, "tcp", "", "also accept raw framed TCP daemon connections on host:port (dev/test)")
	fs.StringVar(&f.adminToken, "admin-token", os.Getenv("EPHEMERA_ADMIN_TOKEN"), "admin API token (or EPHEMERA_ADMIN_TOKEN); empty disables admin routes")
	fs.Int64Var(&f.cacheMB, "cache-mb", 256, "layer-B volatile cache budget in MiB (0 disables replay)")
	fs.Int64Var(&f.awayMB, "away-mb", 512, "layer-C away-prefix budget in MiB (0 disables away mode)")
	fs.Int64Var(&f.maxTTL, "max-ttl", hub.DefaultMaxTTLSec, "longest link lifetime a sender may ask for, in seconds")
	fs.Int64Var(&f.maxSizeMB, "max-size-mb", hub.DefaultMaxSize>>20, "largest file a sender may share, in MiB")
	fs.StringVar(&f.tokens, "tokens", "", "sender tokens file (see `relay token`); senders without a listed token are refused")
	fs.BoolVar(&f.open, "open", false, "let anyone share through this relay, no sender token needed (local testing only)")
	fs.StringVar(&f.tlsCert, "tls-cert", "", "serve HTTPS/WSS with this PEM certificate chain (needs -tls-key)")
	fs.StringVar(&f.tlsKey, "tls-key", "", "PEM private key for -tls-cert")
	fs.StringVar(&f.publicURL, "public-url", "", "base URL receivers open links under, e.g. https://ephemera.run (default: the address the sender dialed)")
	fs.StringVar(&f.proxies, "trusted-proxies", "", "comma-separated IPs or CIDRs of reverse proxies in front of the relay; their X-Forwarded-For gives the client IP for rate limits")
	fs.StringVar(&f.dataDir, "data-dir", "", "keep link reports and account suspensions here across restarts (never file bytes)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkPublicURL(f.publicURL); err != nil {
		return err
	}
	proxies, err := parseProxies(f.proxies)
	if err != nil {
		return err
	}
	if (f.tlsCert == "") != (f.tlsKey == "") {
		return errors.New("-tls-cert and -tls-key go together")
	}
	if f.tcp != "" && !localAddr(f.tcp) {
		// raw framed TCP is plaintext: tokens and owner keys in the clear
		return fmt.Errorf("-tcp %s: the raw TCP listener is plaintext and may only listen on this machine", f.tcp)
	}
	authn, err := senderAuth(f)
	if err != nil {
		return err
	}
	if f.maxSizeMB<<20 > hub.DefaultMaxSize {
		// the daemon and the read path both stop at 5GB today
		return fmt.Errorf("-max-size-mb %d is over the %d MiB shares support", f.maxSizeMB, hub.DefaultMaxSize>>20)
	}

	reports, suspensions, err := moderationState(f.dataDir)
	if err != nil {
		return err
	}

	reg := share.NewRegistry(time.Second)
	bridge := &hub.Bridge{
		Registry:    reg,
		Auth:        authn,
		Limits:      hub.Limits{MaxTTLSec: f.maxTTL, MaxSize: f.maxSizeMB << 20},
		Suspensions: suspensions,
		PublicURL:   strings.TrimSuffix(f.publicURL, "/"),
	}

	// daemon bridge: one WS connection, one share
	daemonBridge := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		bridge.AcceptAt(wsconn.Wrap(c), linkBase(bridge.PublicURL, r))
	})

	cachepkg := cachestore.New(f.cacheMB << 20)
	awaypkg := awaystore.New(f.awayMB << 20)
	if f.cacheMB > 0 {
		reg.OnExpire(cachepkg.DropShare)
	}
	if f.awayMB > 0 {
		reg.OnExpire(awaypkg.DropShare)
		bridge.Away = awaypkg
	}
	surface := relay.New(relay.ServerConfig{
		Registry:       reg,
		Limiter:        ratelimit.New(20, 40), // 20 r/s sustained, burst 40 per IP
		TrustedProxies: proxies,
		AdminToken:     f.adminToken,
		Cache:          cachepkg,
		Away:           awaypkg,

		Reports:       reports,
		ReportLimiter: ratelimit.New(1.0/60, 5), // 5 reports, then 1 a minute, per IP
		Suspensions:   suspensions,
	})

	mux := http.NewServeMux()
	mux.Handle("/daemon", daemonBridge)
	mux.Handle("/", surface)

	if f.tcp != "" {
		ln, err := net.Listen("tcp", f.tcp)
		if err != nil {
			return err
		}
		go acceptLoop(ln, bridge, cmp.Or(bridge.PublicURL, "http://"+dialable(f.addr)))
	}

	srv := &http.Server{
		Addr:              f.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if f.tlsCert != "" {
		// load up front so a bad cert fails before "listening"
		cert, err := tls.LoadX509KeyPair(f.tlsCert, f.tlsKey)
		if err != nil {
			return err
		}
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		fmt.Fprintf(os.Stderr, "relay listening on %s (https, wss)\n", f.addr)
		return srv.ListenAndServeTLS("", "")
	}
	if !localAddr(f.addr) {
		fmt.Fprintln(os.Stderr, "warning: serving plain HTTP off this machine: put a TLS proxy in front, or pass -tls-cert and -tls-key")
	}
	fmt.Fprintf(os.Stderr, "relay listening on %s\n", f.addr)
	return srv.ListenAndServe()
}

// checkPublicURL accepts an empty -public-url or an http(s) base
// with a host and no query.
func checkPublicURL(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("-public-url %q: want http(s)://host[:port][/path]", s)
	}
	return nil
}

// parseProxies reads -trusted-proxies: IPs (one address) or CIDRs.
func parseProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if p, err := netip.ParsePrefix(item); err == nil {
			out = append(out, p.Masked())
			continue
		}
		ip, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("-trusted-proxies: %q is not an IP or CIDR", item)
		}
		out = append(out, netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()))
	}
	return out, nil
}

// linkBase is the base of the links a daemon connecting through r
// should print: -public-url when set, else the address it dialed.
// Behind a TLS proxy the Host is the proxy's public one; the scheme
// comes from the connection or the proxy's X-Forwarded-Proto. The
// header only shapes the link shown to that same sender.
func linkBase(publicURL string, r *http.Request) string {
	if publicURL != "" {
		return publicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// moderationState opens the report queue and suspension list, in
// dataDir when set and in memory otherwise.
func moderationState(dataDir string) (*abuse.Reports, *abuse.Suspensions, error) {
	var reportsPath, suspendedPath string
	if dataDir == "" {
		fmt.Fprintln(os.Stderr, "warning: no -data-dir: link reports and account suspensions are lost when the relay restarts")
	} else {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, nil, err
		}
		reportsPath = filepath.Join(dataDir, "reports.jsonl")
		suspendedPath = filepath.Join(dataDir, "suspended.txt")
	}
	reports, err := abuse.NewReports(reportsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reports: %w", err)
	}
	suspensions, err := abuse.NewSuspensions(suspendedPath)
	if err != nil {
		return nil, nil, fmt.Errorf("suspensions: %w", err)
	}
	return reports, suspensions, nil
}

// localAddr reports whether a listen address only accepts
// connections from this machine. An empty host (":7100") listens on
// every interface.
func localAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	return err == nil && host != "" && daemon.IsLocalHost(host)
}

// senderAuth picks the relay's sender authentication. Secure by
// default: a relay with neither a tokens file nor -open refuses to
// start rather than quietly letting anyone share through it.
func senderAuth(f serveFlags) (hub.Authenticator, error) {
	switch {
	case f.tokens != "" && f.open:
		return nil, errors.New("-tokens and -open are mutually exclusive")
	case f.tokens != "":
		return auth.LoadStatic(f.tokens)
	case f.open:
		fmt.Fprintln(os.Stderr, "warning: -open: anyone who can reach this relay can share through it")
		return nil, nil
	}
	return nil, errors.New("relay serve needs -tokens <file> (create tokens with `relay token <name>`) or -open to let anyone share")
}

// runToken mints a sender token: the token goes to the sender, the
// hash line goes in the relay's tokens file.
func runToken(args []string) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("file", "", "append the hash line to this tokens file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || !auth.ValidName(fs.Arg(0)) {
		return errors.New("usage: relay token [-file tokens] <name>  (name: no spaces or #)")
	}
	name := fs.Arg(0)
	token := auth.NewToken()
	line := name + " " + auth.Hash(token)

	if *file != "" {
		fh, err := os.OpenFile(*file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(fh, line); err != nil {
			fh.Close()
			return err
		}
		if err := fh.Close(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "added %s to %s (restart the relay to pick it up)\n", name, *file)
	} else {
		fmt.Fprintf(os.Stderr, "add this line to the relay's tokens file:\n  %s\n", line)
	}
	fmt.Fprintf(os.Stderr, "sender token for %s (shown once; the sender runs `ephemera login`):\n", name)
	fmt.Println(token)
	return nil
}

// acceptLoop bridges raw framed TCP daemon connections (dev/test);
// their links live under linkBase, the relay's HTTP address.
func acceptLoop(ln net.Listener, bridge *hub.Bridge, linkBase string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			bridge.AcceptAt(conn, linkBase)
		}()
	}
}

// dialable turns a listen address into one a browser can open:
// ":7100" (every interface) becomes "localhost:7100".
func dialable(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "" {
		return addr
	}
	return net.JoinHostPort("localhost", port)
}

func init() {
	register("serve", runServe)
	register("token", runToken)
}
