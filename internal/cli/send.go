package cli

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ephemera.run/ephemera/internal/daemon"
)

// publicRelay is the relay commands talk to unless told otherwise.
const publicRelay = "ephemera.run"

// defaultRelay is publicRelay, or EPHEMERA_RELAY when it's set: a host
// (a bare host means wss://host/daemon) or a full endpoint.
func defaultRelay() string { return cmp.Or(os.Getenv("EPHEMERA_RELAY"), publicRelay) }

// relayBase turns a relay as senders name it (ephemera.run,
// wss://host/daemon) into the base URL of its HTTP API.
func relayBase(relay string) string {
	switch {
	case strings.HasPrefix(relay, "wss://"):
		relay = "https://" + strings.TrimPrefix(relay, "wss://")
	case strings.HasPrefix(relay, "ws://"):
		relay = "http://" + strings.TrimPrefix(relay, "ws://")
	case !strings.Contains(relay, "://"):
		relay = "https://" + relay
	}
	return strings.TrimSuffix(strings.TrimSuffix(relay, "/"), "/daemon")
}

// stateDir is where active-share records live, so `ephemera resume`
// can restart them after a reboot or crash.
func stateDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ephemera"), nil
}

// openState returns the share state dir, best-effort: nil when the
// platform has no config dir (share without resume, not an error).
func openState() *daemon.StateDir {
	dir, err := stateDir()
	if err != nil {
		return nil
	}
	sd, err := daemon.NewStateDir(dir)
	if err != nil {
		return nil
	}
	return sd
}

// dialFunc builds the reconnect dialer for a relay endpoint.
func dialFunc(relay string) func() (net.Conn, error) {
	return func() (net.Conn, error) { return daemon.DialRelay(relay) }
}

// sendFlags holds the flags of the send subcommand.
type sendFlags struct {
	relay    string
	ttl      int64
	cache    bool
	away     int64 // MiB of prefix to upload for away mode; 0 = off
	upload   bool  // send the whole file to the relay in the background
	json     bool  // JSON events on stdout instead of the bare link
	insecure bool
}

// insecureUsage is the -insecure flag's help, shared by commands that
// talk to a relay.
const insecureUsage = "allow a plaintext connection to a relay off this machine (your token travels unencrypted)"

// checkRelay refuses a plaintext relay endpoint off this machine
// before any share starts, so the mistake shows up at once instead of
// as a token sent in the clear.
func checkRelay(endpoint string, insecure bool) error {
	u, err := daemon.RelayURL(endpoint)
	if err != nil {
		return err
	}
	return daemon.CheckTransport(u, insecure)
}

// runSend validates the file, connects to the relay, and runs the
// share loop until its TTL expires. Keeps the machine awake and the
// state record so resume works.
func runSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f sendFlags
	fs.StringVar(&f.relay, "relay", defaultRelay(), "relay: a host like ephemera.run, or wss://host/daemon (ws:// and tcp:// only to this machine); default EPHEMERA_RELAY or ephemera.run")
	fs.Int64Var(&f.ttl, "ttl", 3600, "link lifetime in seconds")
	fs.BoolVar(&f.cache, "cache", false, "let the relay replay already-watched bytes from RAM while you're offline")
	fs.BoolVar(&f.upload, "upload", true, "upload the whole file to the relay in the background, so the link keeps working after you go offline (-upload=false: stream from this machine only)")
	fs.Int64Var(&f.away, "away", 0, "if the relay won't keep the upload, upload this many MiB of the file's start so people can begin watching while you're offline")
	fs.BoolVar(&f.json, "json", false, jsonUsage)
	fs.BoolVar(&f.insecure, "insecure", false, insecureUsage)
	if err := parse(fs, args); err != nil {
		return err
	}
	out := &sendOutput{json: f.json}
	if err := checkRelay(f.relay, f.insecure); err != nil {
		return out.fail(err)
	}
	if fs.NArg() != 1 {
		return out.fail(usage("usage: ephemera send [-relay endpoint] [-ttl seconds] [-json] <file>"))
	}
	path := fs.Arg(0)

	sh, err := daemon.ValidateShare(path, daemon.DetectMIME)
	if err != nil {
		return out.fail(err)
	}
	sh.Cache = f.cache
	sh.Upload = f.upload
	if f.away > 0 {
		sh.AwayPrefixBytes = f.away << 20
	}

	fmt.Fprintf(os.Stderr, "sharing %s (%d bytes, %s) for %ds; connecting to the relay…\n", sh.Name, sh.Size, sh.MIME, f.ttl)

	return out.fail(runShares([]daemon.Share{sh}, []int64{f.ttl}, f.relay, out))
}

// jsonUsage is the -json flag's help, shared by send and resume.
const jsonUsage = "print JSON events on stdout, one per line (link, upload, uploaded, upload_refused, expired, error) instead of the bare link"

// runResume restarts every unexpired share recorded in the state
// dir, with their original link ids and remaining TTLs.
func runResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	relay := fs.String("relay", defaultRelay(), "relay (default EPHEMERA_RELAY or ephemera.run)")
	asJSON := fs.Bool("json", false, jsonUsage)
	insecure := fs.Bool("insecure", false, insecureUsage)
	if err := parse(fs, args); err != nil {
		return err
	}
	out := &sendOutput{json: *asJSON}
	if err := checkRelay(*relay, *insecure); err != nil {
		return out.fail(err)
	}

	sd := openState()
	if sd == nil {
		return out.fail(errors.New("no state dir on this platform"))
	}
	shares, ttls, err := sd.Active()
	if err != nil {
		return out.fail(err)
	}
	if len(shares) == 0 {
		return out.fail(daemon.ErrNoActiveShares)
	}
	for i, sh := range shares {
		fmt.Fprintf(os.Stderr, "resuming %s (%ds left)\n", sh.Name, ttls[i])
	}
	return out.fail(runShares(shares, ttls, *relay, out))
}

// runShares drives one or more concurrent share loops with shared
// keep-awake and state.
func runShares(shares []daemon.Share, ttls []int64, relay string, out *sendOutput) error {
	ka := daemon.NewKeepAwake()
	sd := openState()
	token, err := senderToken()
	if err != nil {
		return err
	}

	errCh := make(chan error, len(shares))
	named := len(shares) > 1
	for i, sh := range shares {
		go func(sh daemon.Share, ttl int64) {
			r := &shareReporter{out: out, sh: sh, named: named, expires: time.Now().Add(time.Duration(ttl) * time.Second)}
			cfg := daemon.RunConfig{
				Dial:      dialFunc(relay),
				TTLSec:    ttl,
				KeepAwake: ka,
				State:     sd,
				Token:     token,
				OnLink:    r.link,
				OnUpload:  r.upload,
			}
			err := daemon.Run(sh, cfg)
			if err == nil && !r.done.Load() {
				r.expired()
			}
			errCh <- err
		}(sh, ttls[i])
	}
	var firstErr error
	for range shares {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// sendOutput is what send and resume report. Scripts get the link on
// stdout, one line per share; with -json they get one JSON event per
// line instead. Everything meant for people goes to stderr.
type sendOutput struct {
	json bool
	mu   sync.Mutex // shares report concurrently
}

// event writes one JSON event line on stdout, in -json mode.
func (o *sendOutput) event(name string, sh daemon.Share, fields map[string]any) {
	if !o.json {
		return
	}
	ev := map[string]any{"event": name}
	if sh.ID != "" {
		ev["share_id"] = sh.ID
	}
	for k, v := range fields {
		ev[k] = v
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	json.NewEncoder(os.Stdout).Encode(ev)
}

// fail reports err as an error event in -json mode and returns it, so
// the exit code and stderr carry it as usual.
func (o *sendOutput) fail(err error) error {
	if err != nil {
		code := map[int]string{ExitUsage: "usage", ExitRefused: "refused"}[ExitCode(err)]
		o.event("error", daemon.Share{}, map[string]any{"code": cmp.Or(code, "error"), "message": err.Error()})
	}
	return err
}

// shareReporter reports one share: its link once the relay accepts it
// (before that the link would open nothing), and its background
// upload when it starts, every tenth of the way or every 30s, and when
// it's done.
type shareReporter struct {
	out     *sendOutput
	sh      daemon.Share
	named   bool // several shares (resume): say which file each line is about
	expires time.Time

	mu      sync.Mutex // an old session's uploader can overlap a redial
	started time.Time
	from    int64 // bytes already on the relay when this run began
	lastAt  time.Time
	lastPct int64
	refused bool
	done    atomic.Bool
}

func (r *shareReporter) suffix() string {
	if r.named {
		return " (" + r.sh.Name + ")"
	}
	return ""
}

func (r *shareReporter) link(link string) {
	if r.out.json {
		r.out.event("link", r.sh, map[string]any{
			"name": r.sh.Name, "size": r.sh.Size, "mime": r.sh.MIME,
			"link": link, "file_url": link + "/file",
			"expires": r.expires.UTC().Format(time.RFC3339),
		})
		return
	}
	if r.named {
		fmt.Fprintf(os.Stderr, "link for %s: %s\n", r.sh.Name, link)
	}
	r.out.mu.Lock()
	fmt.Println(link)
	r.out.mu.Unlock()
}

func (r *shareReporter) expired() {
	fmt.Fprintf(os.Stderr, "link expired%s; nothing left on the relay\n", r.suffix())
	r.out.event("expired", r.sh, nil)
}

func (r *shareReporter) upload(p daemon.UploadProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	switch {
	case p.Refused:
		if !r.refused {
			r.refused = true
			fmt.Fprintf(os.Stderr, "this relay isn't keeping uploads right now%s: the link streams from this machine, so keep it running\n", r.suffix())
			r.out.event("upload_refused", r.sh, nil)
		}
		return
	case p.Have >= p.Size:
		if r.done.CompareAndSwap(false, true) {
			fmt.Fprintf(os.Stderr, "uploaded%s: the relay has the whole file. You can close this; the link works until %s\n",
				r.suffix(), r.expires.Format("Jan 2 15:04"))
			r.out.event("uploaded", r.sh, map[string]any{"size": p.Size, "expires": r.expires.UTC().Format(time.RFC3339)})
		}
		return
	case r.started.IsZero():
		r.started, r.from, r.lastAt = now, p.Have, now
		fmt.Fprintf(os.Stderr, "uploading in the background%s (%s); the link already works\n", r.suffix(), sizeMB(p.Size))
		r.out.event("upload", r.sh, map[string]any{"have": p.Have, "size": p.Size})
		return
	}
	pct := p.Have * 100 / p.Size
	if pct/10 == r.lastPct/10 && now.Sub(r.lastAt) < 30*time.Second {
		return
	}
	r.lastPct, r.lastAt = pct, now
	rate := float64(p.Have-r.from) / max(now.Sub(r.started).Seconds(), 0.001)
	fmt.Fprintf(os.Stderr, "uploaded %d%%%s: %s of %s at %s/s\n", pct, r.suffix(), sizeMB(p.Have), sizeMB(p.Size), sizeMB(int64(rate)))
	r.out.event("upload", r.sh, map[string]any{"have": p.Have, "size": p.Size, "bytes_per_sec": int64(rate)})
}

// sizeMB renders a byte count for people.
func sizeMB(n int64) string {
	if n < 10<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%d MB", n/1e6)
}

func init() {
	register("send", runSend)
	register("resume", runResume)
}
