package cli

import (
	"fmt"
	"os"
)

// Run dispatches the command line for both binaries.
// Subcommands are added as phases land; for now it exists so both
// mains build and so there is one place for shared flags.
func Run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return fmt.Errorf("no command given")
	}
	switch args[0] {
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, "usage: <binary> <command>")
}
