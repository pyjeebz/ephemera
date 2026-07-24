package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
)

// The box lifecycle verbs. A box is the unit you work with: give it a name and
// it is a persistent computer that survives stop/start; leave it unnamed and it
// is a throwaway that is gone when you remove it.

func cmdNew(argv []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	addr := daemonAddr(fs)
	net := fs.Bool("net", false, "give a throwaway box a network (named boxes get one when the daemon can)")
	cpus := fs.Int("cpus", 0, "vCPUs for a throwaway box (0 = daemon default)")
	mem := fs.Int("mem", 0, "memory in MiB for a throwaway box (0 = daemon default)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera new [name] [flags]\n\n"+
			"Spins up a box. Give it a name to keep it (a persistent computer you can\n"+
			"stop and start with its state intact); omit the name for a throwaway.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl := client.New(*addr)

	if fs.NArg() >= 1 {
		name := fs.Arg(0)
		c, err := cl.CreateComputer(ctx, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "ephemera: box %q is up — 'ephemera ssh %s' to use it\n", c.Name, c.Name)
		fmt.Println(c.Name)
		return nil
	}

	m, err := cl.Create(ctx, api.CreateRequest{VCPUs: *cpus, MemMiB: *mem, Network: *net})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "ephemera: throwaway box %s is up — 'ephemera ssh %s' to use it\n", m.ID, m.ID)
	fmt.Println(m.ID)
	return nil
}

func cmdStop(argv []string) error {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	addr := daemonAddr(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ephemera stop <box>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := client.New(*addr).StopComputer(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "ephemera: box %q stopped — its disk is kept, 'ephemera start %s' to resume\n", c.Name, c.Name)
	return nil
}

func cmdStart(argv []string) error {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	addr := daemonAddr(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ephemera start <box>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c, err := client.New(*addr).StartComputer(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "ephemera: box %q is up — 'ephemera ssh %s'\n", c.Name, c.Name)
	return nil
}

func cmdDesktop(_ []string) error {
	return fmt.Errorf("the graphical desktop is coming in a later phase; for now, 'ephemera ssh <box>' gives you a terminal")
}
