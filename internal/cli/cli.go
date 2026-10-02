package cli

import (
	"fmt"
	"os"
	"sort"
)

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
		return fmt.Errorf("no command given")
	}
	if fn, ok := commands[args[0]]; ok {
		return fn(args[1:])
	}
	printUsage(os.Stderr)
	return fmt.Errorf("unknown command %q", args[0])
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
