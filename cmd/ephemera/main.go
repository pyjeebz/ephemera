package main

import (
	"fmt"
	"os"

	"ephemera.run/ephemera/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil && cli.ExitCode(err) != cli.ExitOK {
		fmt.Fprintln(os.Stderr, "ephemera:", err)
		os.Exit(cli.ExitCode(err))
	}
}
