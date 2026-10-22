package cli

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"

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
	fs.Int64Var(&f.away, "away", 0, "upload this many MiB of the file's start so people can begin watching while you're offline")
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
	for i, sh := range shares {
		go func(sh daemon.Share, ttl int64) {
			cfg := daemon.RunConfig{
				Dial:      dialFunc(relay),
				TTLSec:    ttl,
				KeepAwake: ka,
				State:     sd,
				Token:     token,
				OnLink:    printLink(sh, len(shares) > 1),
			}
			if err := daemon.Run(sh, cfg); err != nil {
				errCh <- err
				return
			}
			errCh <- nil
		}(sh, ttls[i])
	}
	var firstErr error
	for range shares {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		fmt.Fprintln(os.Stderr, "all shares expired; nothing left on the relay")
	}
	return firstErr
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
