package cli

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"ephemera.run/ephemera/internal/daemon"
)

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
	fs.StringVar(&f.relay, "relay", "ws://localhost:7100/daemon", "relay endpoint (wss://host/daemon; ws:// and tcp:// only to this machine)")
	fs.Int64Var(&f.ttl, "ttl", 3600, "link lifetime in seconds")
	fs.BoolVar(&f.cache, "cache", false, "let the relay replay already-watched bytes from RAM while you're offline")
	fs.BoolVar(&f.upload, "upload", true, "upload the whole file to the relay in the background, so the link keeps working after you go offline (-upload=false: stream from this machine only)")
	fs.Int64Var(&f.away, "away", 0, "if the relay won't keep the upload, upload this many MiB of the file's start so people can begin watching while you're offline")
	fs.BoolVar(&f.insecure, "insecure", false, insecureUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkRelay(f.relay, f.insecure); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: ephemera send [-relay endpoint] [-ttl seconds] <file>")
	}
	path := fs.Arg(0)

	sh, err := daemon.ValidateShare(path, daemon.DetectMIME)
	if err != nil {
		return err
	}
	sh.Cache = f.cache
	sh.Upload = f.upload
	if f.away > 0 {
		sh.AwayPrefixBytes = f.away << 20
	}

	fmt.Fprintf(os.Stderr, "sharing %s (%d bytes, %s) for %ds; connecting to the relay…\n", sh.Name, sh.Size, sh.MIME, f.ttl)

	return runShares([]daemon.Share{sh}, []int64{f.ttl}, f.relay)
}

// runResume restarts every unexpired share recorded in the state
// dir, with their original link ids and remaining TTLs.
func runResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	relay := fs.String("relay", "ws://localhost:7100/daemon", "relay endpoint")
	insecure := fs.Bool("insecure", false, insecureUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkRelay(*relay, *insecure); err != nil {
		return err
	}

	sd := openState()
	if sd == nil {
		return errors.New("no state dir on this platform")
	}
	shares, ttls, err := sd.Active()
	if err != nil {
		return err
	}
	if len(shares) == 0 {
		return daemon.ErrNoActiveShares
	}
	for i, sh := range shares {
		fmt.Fprintf(os.Stderr, "resuming %s (%ds left)\n", sh.Name, ttls[i])
	}
	return runShares(shares, ttls, *relay)
}

// runShares drives one or more concurrent share loops with shared
// keep-awake and state.
func runShares(shares []daemon.Share, ttls []int64, relay string) error {
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
			up := &uploadPrinter{name: sh.Name, named: named, expires: time.Now().Add(time.Duration(ttl) * time.Second)}
			cfg := daemon.RunConfig{
				Dial:      dialFunc(relay),
				TTLSec:    ttl,
				KeepAwake: ka,
				State:     sd,
				Token:     token,
				OnLink:    printLink(sh, named),
				OnUpload:  up.print,
			}
			err := daemon.Run(sh, cfg)
			if err == nil && !up.done.Load() {
				fmt.Fprintf(os.Stderr, "link expired%s; nothing left on the relay\n", up.suffix())
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

// uploadPrinter reports a share's background upload on stderr: when
// it starts, every tenth of the way or every 30s, and when it's done.
type uploadPrinter struct {
	name    string
	named   bool
	expires time.Time

	mu      sync.Mutex // an old session's uploader can overlap a redial
	started time.Time
	from    int64 // bytes already on the relay when this run began
	lastAt  time.Time
	lastPct int64
	refused bool
	done    atomic.Bool
}

func (u *uploadPrinter) suffix() string {
	if u.named {
		return " (" + u.name + ")"
	}
	return ""
}

func (u *uploadPrinter) print(p daemon.UploadProgress) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now()
	switch {
	case p.Refused:
		if !u.refused {
			u.refused = true
			fmt.Fprintf(os.Stderr, "this relay isn't keeping uploads right now%s: the link streams from this machine, so keep it running\n", u.suffix())
		}
		return
	case p.Have >= p.Size:
		if u.done.CompareAndSwap(false, true) {
			fmt.Fprintf(os.Stderr, "uploaded%s: the relay has the whole file. You can close this; the link works until %s\n",
				u.suffix(), u.expires.Format("Jan 2 15:04"))
		}
		return
	case u.started.IsZero():
		u.started, u.from, u.lastAt = now, p.Have, now
		fmt.Fprintf(os.Stderr, "uploading in the background%s (%s); the link already works\n", u.suffix(), sizeMB(p.Size))
		return
	}
	pct := p.Have * 100 / p.Size
	if pct/10 == u.lastPct/10 && now.Sub(u.lastAt) < 30*time.Second {
		return
	}
	u.lastPct, u.lastAt = pct, now
	rate := float64(p.Have-u.from) / max(now.Sub(u.started).Seconds(), 0.001)
	fmt.Fprintf(os.Stderr, "uploaded %d%%%s: %s of %s at %s/s\n", pct, u.suffix(), sizeMB(p.Have), sizeMB(p.Size), sizeMB(int64(rate)))
}

// sizeMB renders a byte count for people.
func sizeMB(n int64) string {
	if n < 10<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%d MB", n/1e6)
}

// printLink prints a share's link once the relay accepts it: before
// that, opening the link would only find nothing. With several shares
// (resume) each line names its file.
func printLink(sh daemon.Share, named bool) func(string) {
	return func(link string) {
		if named {
			fmt.Fprintf(os.Stderr, "link: %s  (%s)\n", link, sh.Name)
			return
		}
		fmt.Fprintf(os.Stderr, "link: %s\n", link)
	}
}

func init() {
	register("send", runSend)
	register("resume", runResume)
}
