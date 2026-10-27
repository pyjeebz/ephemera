package cli

import (
	"errors"
	"flag"
	"fmt"
	"testing"

	"ephemera.run/ephemera/internal/daemon"
)

func TestExitCode(t *testing.T) {
	refused := &daemon.RefusedError{Code: "denied", Message: "no"}
	for err, want := range map[error]int{
		nil:                                  ExitOK,
		flag.ErrHelp:                         ExitOK,
		usage("usage: ephemera send <file>"): ExitUsage,
		refused:                              ExitRefused,
		fmt.Errorf("share: %w", refused):     ExitRefused,
		errors.New("connection refused"):     ExitError,
		daemon.ErrNoActiveShares:             ExitError,
	} {
		if got := ExitCode(err); got != want {
			t.Errorf("ExitCode(%v) = %d, want %d", err, got, want)
		}
	}
	if err := Run([]string{"no-such-command"}); ExitCode(err) != ExitUsage {
		t.Errorf("unknown command: %v, exit %d", err, ExitCode(err))
	}
}
