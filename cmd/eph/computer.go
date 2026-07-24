package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/pyjeebz/ephemera/internal/client"
)

// The `eph computer` family: named, persistent machines you keep around and use
// like a laptop. Create makes and boots one; stop parks it (its disk survives);
// start brings it back with its state; rm deletes it for good.

func cmdComputer(argv []string) error {
	if len(argv) == 0 {
		computerUsage()
		return fmt.Errorf("need a subcommand")
	}
	sub, rest := argv[0], argv[1:]
	switch sub {
	case "create":
		return computerCreate(rest)
	case "ls":
		return computerList(rest)
	case "start":
		return computerStart(rest)
	case "stop":
		return computerStop(rest)
	case "rm":
		return computerRm(rest)
	case "-h", "--help", "help":
		computerUsage()
		return nil
	default:
		computerUsage()
		return fmt.Errorf("unknown computer subcommand %q", sub)
	}
}

func computerUsage() {
	fmt.Fprint(os.Stderr, `usage: eph computer <subcommand> [flags]

  create <name>   make a persistent computer and start it
  ls              list computers and whether they are running
  start <name>    boot a stopped computer from its disk (state intact)
  stop <name>     stop a computer, keeping its disk
  rm <name>       delete a computer and its disk for good

Then 'eph shell <name>' to use it.
`)
}

func computerCreate(argv []string) error {
	fs := flag.NewFlagSet("computer create", flag.ExitOnError)
	addr := daemonAddr(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: eph computer create <name>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c, err := client.New(*addr).CreateComputer(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "eph: computer %q created and running — 'eph shell %s' to use it\n", c.Name, c.Name)
	fmt.Println(c.Name)
	return nil
}

func computerList(argv []string) error {
	fs := flag.NewFlagSet("computer ls", flag.ExitOnError)
	addr := daemonAddr(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	computers, err := client.New(*addr).ListComputers(ctx)
	if err != nil {
		return err
	}
	if len(computers) == 0 {
		fmt.Fprintln(os.Stderr, "no computers — 'eph computer create <name>' to make one")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tADDRESS\tAGE")
	for _, c := range computers {
		state := "stopped"
		addr := "-"
		if c.Running {
			state = "running"
			if c.GuestIP != "" {
				addr = c.GuestIP
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Name, state, addr, time.Since(c.CreatedAt).Round(time.Second))
	}
	return tw.Flush()
}

func computerStart(argv []string) error {
	fs := flag.NewFlagSet("computer start", flag.ExitOnError)
	addr := daemonAddr(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: eph computer start <name>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c, err := client.New(*addr).StartComputer(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "eph: computer %q running\n", c.Name)
	return nil
}

func computerStop(argv []string) error {
	fs := flag.NewFlagSet("computer stop", flag.ExitOnError)
	addr := daemonAddr(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: eph computer stop <name>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := client.New(*addr).StopComputer(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "eph: computer %q stopped (its disk is kept)\n", c.Name)
	return nil
}

func computerRm(argv []string) error {
	fs := flag.NewFlagSet("computer rm", flag.ExitOnError)
	addr := daemonAddr(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: eph computer rm <name>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.New(*addr).DeleteComputer(ctx, fs.Arg(0)); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "eph: computer %q deleted\n", fs.Arg(0))
	return nil
}
