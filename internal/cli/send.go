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
	relay string
	ttl   int64
	cache bool
}

// runSend validates the file, connects to the relay, and runs the
// share loop until its TTL expires. Keeps the machine awake and the
// state record so resume works.
func runSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f sendFlags
	fs.StringVar(&f.relay, "relay", "ws://localhost:7100/daemon", "relay endpoint (ws:// host:port/daemon or tcp://host:port)")
	fs.Int64Var(&f.ttl, "ttl", 3600, "link lifetime in seconds")
	fs.BoolVar(&f.cache, "cache", false, "let the relay replay already-watched bytes from RAM while you're offline")
	if err := fs.Parse(args); err != nil {
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

	fmt.Fprintf(os.Stderr, "sharing %s (%d bytes, %s) for %ds\n", sh.Name, sh.Size, sh.MIME, f.ttl)
	fmt.Fprintf(os.Stderr, "link: https://ephemera.run/%s\n", sh.ID)

	return runShares([]daemon.Share{sh}, []int64{f.ttl}, f.relay)
}

// runResume restarts every unexpired share recorded in the state
// dir, with their original link ids and remaining TTLs.
func runResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	relay := fs.String("relay", "ws://localhost:7100/daemon", "relay endpoint")
	if err := fs.Parse(args); err != nil {
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
		fmt.Fprintf(os.Stderr, "resuming %s (link /%s, %ds left)\n", sh.Name, sh.ID, ttls[i])
	}
	return runShares(shares, ttls, *relay)
}

// runShares drives one or more concurrent share loops with shared
// keep-awake and state.
func runShares(shares []daemon.Share, ttls []int64, relay string) error {
	ka := daemon.NewKeepAwake()
	sd := openState()

	errCh := make(chan error, len(shares))
	for i, sh := range shares {
		go func(sh daemon.Share, ttl int64) {
			cfg := daemon.RunConfig{
				Dial:      dialFunc(relay),
				TTLSec:    ttl,
				KeepAwake: ka,
				State:     sd,
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
	fmt.Fprintln(os.Stderr, "all shares expired; nothing left on the relay")
	return firstErr
}

func init() {
	register("send", runSend)
	register("resume", runResume)
}
