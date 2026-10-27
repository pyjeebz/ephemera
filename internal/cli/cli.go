package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"ephemera.run/ephemera/internal/daemon"
)

// Exit codes, so scripts and agents can tell outcomes apart without
// reading messages.
const (
	ExitOK      = 0 // done: shared until expiry, uploaded, or the command succeeded
	ExitError   = 1 // anything else: the network, the file, the relay unreachable
	ExitUsage   = 2 // bad flags or arguments
	ExitRefused = 3 // the relay turned the share down: token, limits, a killed link
)

// ExitCode maps an error from Run to the process exit code.
func ExitCode(err error) int {
	var refused *daemon.RefusedError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return ExitOK
	case errors.As(err, &usageError{}):
		return ExitUsage
	case errors.As(err, &refused):
		return ExitRefused
	}
	return ExitError
}

// usageError is a command line that doesn't make sense.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// usage reports a bad command line with its usage message.
func usage(msg string) error { return usageError{msg} }

// parse parses a command's flags, reporting a bad flag as a usage
// error. The flag package has already printed what went wrong.
func parse(fs *flag.FlagSet, args []string) error {
	err := fs.Parse(args)
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		return usageError{err.Error()}
	}
	return err
}

// commands is the subcommand table. Each command registers itself
// via init(); Run dispatches on the first argument.
var commands = map[string]func([]string) error{}

// register adds a subcommand. Called from init().
func register(name string, fn func([]string) error) {
	commands[name] = fn
}

// Run dispatches the command line for both binaries.
func Run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return usage("no command given")
	}
	if fn, ok := commands[args[0]]; ok {
		return fn(args[1:])
	}
	printUsage(os.Stderr)
	return usage(fmt.Sprintf("unknown command %q", args[0]))
}

func printUsage(w *os.File) {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "usage: <binary> <command>")
	fmt.Fprintln(w, "commands:", names)
}
