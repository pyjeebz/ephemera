package cli

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"

	"ephemera.run/ephemera/internal/daemon"
)

// sendFlags holds the flags of the send subcommand.
type sendFlags struct {
	relay string
	ttl   int64
}

// runSend validates the file, connects to the relay, and runs the
// share loop until its TTL expires.
func runSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f sendFlags
	fs.StringVar(&f.relay, "relay", "ephemera.run:7100", "relay address (host:port)")
	fs.Int64Var(&f.ttl, "ttl", 3600, "link lifetime in seconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: ephemera send [-relay host:port] [-ttl seconds] <file>")
	}
	path := fs.Arg(0)

	sh, err := daemon.ValidateShare(path, daemon.DetectMIME)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "sharing %s (%d bytes, %s) for %ds\n", sh.Name, sh.Size, sh.MIME, f.ttl)

	err = daemon.Run(sh, daemon.RunConfig{
		Dial: func() (net.Conn, error) {
			return net.Dial("tcp", f.relay)
		},
		TTLSec: f.ttl,
	})
	if err != nil && !errors.Is(err, daemon.ErrTTLExpired) {
		return err
	}
	fmt.Fprintln(os.Stderr, "share expired; nothing left on the relay")
	return nil
}

func init() {
	register("send", runSend)
}
