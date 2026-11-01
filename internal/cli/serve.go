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
	"ephemera.run/ephemera/internal/playback"
	"ephemera.run/ephemera/internal/ratelimit"
	"ephemera.run/ephemera/internal/relay"
	"ephemera.run/ephemera/internal/share"
	"ephemera.run/ephemera/internal/signin"
	"ephemera.run/ephemera/internal/upload"
	"ephemera.run/ephemera/internal/wsconn"
)

// serveFlags holds the flags of the relay serve subcommand.
type serveFlags struct {
	addr       string
	tcp        string // optional raw TCP bridge listener (host:port)
	adminToken string
	cacheMB    int64
	awayMB     int64
	uploadGB   int64 // background upload disk budget
	maxTTL     int64 // seconds
	maxSizeMB  int64
	tokens     string // sender tokens file
	open       bool   // no sender auth at all
	tlsCert    string // PEM certificate chain; with tlsKey, serve HTTPS
	tlsKey     string
	dataDir    string // operator state: reports, suspensions, uploads
	publicURL  string // base of the links receivers open
	proxies    string // trusted reverse proxies, comma-separated
	ipHeader   string // header the proxy puts the client address in

	guests      bool  // senders without a token share as guests
	guestTTL    int64 // seconds
	guestSizeMB int64
	guestStarts float64 // new guest shares per address per hour

	githubID string // GitHub OAuth app; its secret is in EPHEMERA_GITHUB_SECRET
	googleID string // Google OAuth client; its secret is in EPHEMERA_GOOGLE_SECRET
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
	fs.Int64Var(&f.uploadGB, "upload-gb", 0, "disk budget in GiB for background uploads, kept in -data-dir until each link expires (0 disables them)")
	fs.Int64Var(&f.maxTTL, "max-ttl", hub.DefaultMaxTTLSec, "longest link lifetime a sender may ask for, in seconds")
	fs.Int64Var(&f.maxSizeMB, "max-size-mb", hub.DefaultMaxSize>>20, "largest file a sender may share, in MiB")
	fs.StringVar(&f.tokens, "tokens", "", "sender tokens file (see `relay token`); senders without a listed token are refused")
	fs.BoolVar(&f.open, "open", false, "let anyone share through this relay, no sender token needed (local testing only)")
	fs.StringVar(&f.tlsCert, "tls-cert", "", "serve HTTPS/WSS with this PEM certificate chain (needs -tls-key)")
	fs.StringVar(&f.tlsKey, "tls-key", "", "PEM private key for -tls-cert")
	fs.StringVar(&f.publicURL, "public-url", "", "base URL receivers open links under, e.g. https://ephemera.run (default: the address the sender dialed)")
	fs.StringVar(&f.proxies, "trusted-proxies", "", "comma-separated IPs or CIDRs of reverse proxies in front of the relay; their X-Forwarded-For gives the client IP for rate limits")
	fs.StringVar(&f.ipHeader, "client-ip-header", "", "header the proxy in front sets to the client IP, e.g. Fly-Client-IP; read only from -trusted-proxies if set, else from every request")
	fs.BoolVar(&f.guests, "guests", false, "let senders without a token share as guests, under the -guest-* limits")
	fs.Int64Var(&f.guestTTL, "guest-ttl", 3600, "longest link lifetime a guest may ask for, in seconds")
	fs.Int64Var(&f.guestSizeMB, "guest-size-mb", 1024, "largest file a guest may share, in MiB")
	fs.Float64Var(&f.guestStarts, "guest-shares-per-hour", 10, "new shares one guest address may start an hour")
	fs.StringVar(&f.githubID, "github-client-id", "", "let people sign in with GitHub for a sender token: the OAuth app's client id (secret in EPHEMERA_GITHUB_SECRET; callback <public-url>/auth/github/callback)")
	fs.StringVar(&f.googleID, "google-client-id", "", "let people sign in with Google for a sender token: the OAuth client id (secret in EPHEMERA_GOOGLE_SECRET; callback <public-url>/auth/google/callback)")
	fs.StringVar(&f.dataDir, "data-dir", "", "keep link reports, account suspensions and (with -upload-gb) uploaded files here across restarts")
	if err := parse(fs, args); err != nil {
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
	signIn, accounts, err := signInServer(f, proxies)
	if err != nil {
		return err
	}
	authn, err := senderAuth(f, accounts)
	if err != nil {
		return err
	}
	if f.maxSizeMB<<20 > hub.DefaultMaxSize {
		// the daemon and the read path both stop at 5GB today
		return fmt.Errorf("-max-size-mb %d is over the %d MiB shares support", f.maxSizeMB, hub.DefaultMaxSize>>20)
	}

	if f.uploadGB > 0 && f.dataDir == "" {
		return errors.New("-upload-gb needs -data-dir: uploaded files are kept on disk there")
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
	if f.guests {
		bridge.Guests = &hub.Guests{
			Limits: hub.Limits{MaxTTLSec: min(f.guestTTL, f.maxTTL), MaxSize: min(f.guestSizeMB, f.maxSizeMB) << 20},
			Starts: ratelimit.New(f.guestStarts/3600, max(f.guestStarts, 1)),
		}
	}
	clients := relay.Clients{TrustedProxies: proxies, Header: f.ipHeader}

	// daemon bridge: one WS connection, one share
	daemonBridge := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		bridge.AcceptFrom(wsconn.Wrap(c), linkBase(bridge.PublicURL, r), clients.Key(r))
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
	var uploads *upload.Store
	if f.uploadGB > 0 {
		uploads, err = upload.Open(filepath.Join(f.dataDir, "uploads"), f.uploadGB<<30)
		if err != nil {
			return fmt.Errorf("uploads: %w", err)
		}
		reg.OnExpire(uploads.Drop)
		bridge.Uploads = uploads
		if n := relay.RestoreUploads(reg, uploads); n > 0 {
			fmt.Fprintf(os.Stderr, "serving %d uploaded links kept from before the restart\n", n)
		}
	}
	// how playback went, per view: kept for `ephemera stats` and
	// logged, so the host's log shows it too
	views, senders := playback.NewLog(1000), playback.NewSenders()
	views.OnAdd = logPlayback
	reg.OnExpire(senders.Drop)

	surface := relay.New(relay.ServerConfig{
		Registry: reg,
		Limiter:  ratelimit.New(20, 40), // 20 r/s sustained, burst 40 per IP
		// file bytes: a DuckDB query is a couple of hundred range
		// requests, and got throttled at the page limit
		FileLimiter:    ratelimit.New(200, 400),
		TrustedProxies: proxies,
		ClientIPHeader: f.ipHeader,
		AdminToken:     f.adminToken,
		Cache:          cachepkg,
		Away:           awaypkg,
		Uploads:        uploads,
		Playback:       views,
		Senders:        senders,
		FirstBytes:     playback.NewFirstBytes(1000),

		Policy: senderPolicy(bridge, authn, signIn),

		Reports:       reports,
		ReportLimiter: ratelimit.New(1.0/60, 5), // 5 reports, then 1 a minute, per IP
		Suspensions:   suspensions,
	})

	mux := http.NewServeMux()
	mux.Handle("/daemon", daemonBridge)
	mux.Handle("/", surface)
	if signIn != nil {
		mux.Handle("/auth/", signIn.Handler())
	}

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

// logPlayback writes one line per playback report to stderr.
func logPlayback(v playback.View) {
	line := fmt.Sprintf("playback %s: %s, first frame %dms", v.Share, v.State, v.TTFFMs)
	if v.StartMs > 0 {
		line += fmt.Sprintf(", playing %dms after play", v.StartMs)
	}
	line += fmt.Sprintf(", %d stalls (%.1fs), %d seeks, watched %.0fs", v.Stalls, float64(v.StallMs)/1000, v.Seeks, v.WatchedS)
	if v.Seeks > 0 {
		line += fmt.Sprintf(", %dms a seek", v.SeekWaitMs/v.Seeks)
	}
	if v.BitrateBps > 0 {
		line += fmt.Sprintf(", file %.1f Mbps", float64(v.BitrateBps)/1e6)
	}
	if v.SenderBps > 0 {
		line += fmt.Sprintf(", sender %.1f Mbps", float64(v.SenderBps)/1e6)
	}
	fmt.Fprintf(os.Stderr, "%s, %d%% from the relay\n", line, v.FromRelayPct)
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

// senderAuth picks the relay's sender authentication: tokens from the
// tokens file, then those minted by web sign-in (accounts, may be
// nil). Secure by default: a relay with no way to tell senders apart
// and no -open refuses to start rather than quietly letting anyone
// share through it.
func senderAuth(f serveFlags, accounts *auth.Accounts) (hub.Authenticator, error) {
	if f.open {
		if f.tokens != "" || f.guests {
			return nil, errors.New("-open lets anyone share: it doesn't go with -tokens or -guests")
		}
		fmt.Fprintln(os.Stderr, "warning: -open: anyone who can reach this relay can share through it")
		return nil, nil
	}
	chain := auth.Chain{}
	if f.tokens != "" {
		static, err := auth.LoadStatic(f.tokens)
		if err != nil {
			return nil, err
		}
		chain = append(chain, static)
	}
	if accounts != nil {
		chain = append(chain, accounts)
	}
	if len(chain) == 0 && !f.guests {
		return nil, errors.New("relay serve needs -tokens <file> (create tokens with `relay token <name>`), sign-in (-google-client-id, -github-client-id), -guests, or -open to let anyone share")
	}
	return chain, nil // empty with -guests: everyone is a guest
}

// signInServer sets up web sign-in for the providers configured, or
// returns nil when there are none. Accounts made by signing in live
// in -data-dir; links to the callback need -public-url.
func signInServer(f serveFlags, proxies []netip.Prefix) (*signin.Server, *auth.Accounts, error) {
	var providers []signin.Provider
	for _, p := range []struct{ name, id, env string }{
		{"google", f.googleID, "EPHEMERA_GOOGLE_SECRET"},
		{"github", f.githubID, "EPHEMERA_GITHUB_SECRET"},
	} {
		if p.id == "" {
			continue
		}
		secret := os.Getenv(p.env)
		if secret == "" {
			return nil, nil, fmt.Errorf("-%s-client-id needs the client secret in %s", p.name, p.env)
		}
		if p.name == "google" {
			providers = append(providers, signin.Google(p.id, secret))
		} else {
			providers = append(providers, signin.GitHub(p.id, secret))
		}
	}
	if len(providers) == 0 {
		return nil, nil, nil
	}
	switch {
	case f.open:
		return nil, nil, errors.New("-open lets anyone share: there's nothing to sign in for")
	case f.dataDir == "":
		return nil, nil, errors.New("sign-in needs -data-dir: accounts are kept there")
	case f.publicURL == "":
		return nil, nil, errors.New("sign-in needs -public-url: the providers send people back to it")
	}
	accounts, err := auth.OpenAccounts(filepath.Join(f.dataDir, "accounts.json"))
	if err != nil {
		return nil, nil, err
	}
	clients := relay.Clients{TrustedProxies: proxies, Header: f.ipHeader}
	return &signin.Server{
		Providers: providers,
		Accounts:  accounts,
		BaseURL:   f.publicURL,
		Limit:     ratelimit.New(10.0/3600, 10), // 10 sign-ins an hour per address
		Client:    clients.Key,
	}, accounts, nil
}

// senderPolicy describes the bridge's senders for the web page.
func senderPolicy(b *hub.Bridge, authn hub.Authenticator, signIn *signin.Server) relay.SenderPolicy {
	limits := func(l hub.Limits) relay.Limits {
		l = l.Or(b.Limits)
		return relay.Limits{MaxTTLSec: l.TTL(), MaxSize: l.Size()}
	}
	p := relay.SenderPolicy{Max: limits(b.Limits)}
	if signIn != nil {
		p.SignIn = signIn.Names()
	}
	switch {
	case authn == nil: // open relay: everyone shares in full
		p.Guest = &p.Max
	case b.Guests != nil:
		g := limits(b.Guests.Limits)
		p.Guest = &g
	}
	return p
}

// runToken mints a sender token: the token goes to the sender, the
// hash line goes in the relay's tokens file.
func runToken(args []string) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("file", "", "append the hash line to this tokens file")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 || !auth.ValidName(fs.Arg(0)) {
		return usage("usage: relay token [-file tokens] <name>  (name: no spaces or #)")
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
