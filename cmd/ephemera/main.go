package main

import (
	"fmt"
	"os"

	"ephemera.run/ephemera/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ephemera:", err)
		os.Exit(1)
	}
}
