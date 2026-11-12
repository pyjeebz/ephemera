package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/config"
	"github.com/pyjeebz/ephemera/internal/record"
)

// needsHosts reports whether a command talks to hosts, and so needs the
// pinned host keys.
func needsHosts(cmd string) bool {
	switch cmd {
	case "run", "dev", "mcp", "hosts", "router", "routes", "focus":
		return true
	}
	return false
}

// loadPins reads the host keys this CLI has pinned.
func loadPins() (*client.Pins, error) {
	path, err := config.KnownHostsPath()
	if err != nil {
		return nil, err
	}
	pins, err := client.LoadPins(path)
	if err != nil {
		return nil, fmt.Errorf("reading pinned host keys: %w; fix or remove that file", err)
	}
	return pins, nil
}

// cmdHosts lists the account's hosts with their keys and pins, or, with
// "trust", pins the key the central server has for a host now.
func cmdHosts(ctx context.Context, c *client.Client, args []string, stdout, stderr io.Writer) int {
	hosts, err := c.Hosts(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
	}
	if len(args) == 2 && args[0] == "trust" {
		for _, h := range hosts {
			if h.Name != args[1] && h.ID != args[1] {
				continue
			}
			fp, err := record.Fingerprint(h.PublicKey)
			if err != nil {
				fmt.Fprintln(stderr, "ephemera: host", h.Name+":", err)
				return exitFailure
			}
			if err := c.Pins.Set(h.ID, client.Pin{Name: h.Name, Fingerprint: fp, FirstSeen: time.Now().UTC()}); err != nil {
				fmt.Fprintln(stderr, "ephemera:", err)
				return exitFailure
			}
			fmt.Fprintf(stdout, "Pinned %s (%s): %s\n", h.Name, h.ID, fp)
			return 0
		}
		fmt.Fprintf(stderr, "ephemera: no host %q in this account; ephemera hosts lists them\n", args[1])
		return exitFailure
	}
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: ephemera hosts [trust <name-or-id>]")
		return 2
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tONLINE\tKEY\tPINNED")
	for _, h := range hosts {
		fp, err := record.Fingerprint(h.PublicKey)
		if err != nil {
			fp = "?"
		}
		pinned := "not yet; on first use"
		if p, ok := c.Pins.Get(h.ID); ok {
			pinned = "yes"
			if p.Fingerprint != fp {
				pinned = "KEY CHANGED: pinned " + p.Fingerprint
			}
		}
		online := "no"
		if h.Online {
			online = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", h.Name, h.ID, online, fp, pinned)
	}
	tw.Flush()
	return 0
}

// keyChanged reports whether err is a pinned host presenting another key.
func keyChanged(err error) bool {
	var kc *client.HostKeyChangedError
	return errors.As(err, &kc)
}
