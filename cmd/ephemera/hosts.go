package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/config"
	"github.com/pyjeebz/ephemera/internal/record"
)

// needsHosts reports whether a command talks to hosts, and so needs the
// pinned host keys.
func needsHosts(cmd string) bool {
	switch cmd {
	case "run", "dev", "mcp", "hosts", "trust", "router", "routes", "focus":
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

// cmdTrust pins the key the central server has for a host now, by name
// or id.
func cmdTrust(ctx context.Context, c *client.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Usage: ephemera trust <host>")
		return 2
	}
	hosts, err := c.Hosts(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
	}
	for _, h := range hosts {
		if h.Name != args[0] && h.ID != args[0] {
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
	fmt.Fprintf(stderr, "ephemera: no host %q in this account; ephemera hosts lists them\n", args[0])
	return exitFailure
}

// cmdHosts lists the account's hosts with their keys and pins.
func cmdHosts(ctx context.Context, c *client.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: ephemera hosts")
		return 2
	}
	hosts, err := c.Hosts(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tONLINE\tLAST SEEN\tKEY\tPINNED")
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
		online, seen := "no", lastSeen(h)
		if hostOnline(h) {
			online, seen = "yes", "now"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", h.Name, h.ID, online, seen, fp, pinned)
	}
	tw.Flush()
	return 0
}

// keyChanged reports whether err is a pinned host presenting another key.
func keyChanged(err error) bool {
	var kc *client.HostKeyChangedError
	return errors.As(err, &kc)
}

// hostOnline reports whether a host is checking in. A server older than
// this CLI sends neither url nor online, so then it judges by the last
// heartbeat on this machine's clock.
func hostOnline(h api.Host) bool {
	if h.URL == "" {
		return time.Since(h.LastSeen) <= hostTimeout
	}
	return h.Online
}

// lastSeen says when a host last checked in, in words.
func lastSeen(h api.Host) string {
	if h.LastSeen.IsZero() {
		return "never"
	}
	return ago(time.Since(h.LastSeen)) + " ago"
}

// ago rounds a duration to its largest whole unit, up to days.
func ago(d time.Duration) string {
	n, unit := int(d/time.Second), "second"
	switch {
	case d >= 48*time.Hour:
		n, unit = int(d/(24*time.Hour)), "day"
	case d >= 2*time.Hour:
		n, unit = int(d/time.Hour), "hour"
	case d >= 2*time.Minute:
		n, unit = int(d/time.Minute), "minute"
	}
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// noHostNote says why no sandbox can start when none of the account's
// hosts is online, naming them and when they were last seen. It returns ""
// when a host is online.
func noHostNote(hosts []api.Host) string {
	if len(hosts) == 0 {
		return "no host has joined this account yet; add one: https://ephemera.run/docs/guides/hosts"
	}
	latest := hosts[0]
	for _, h := range hosts {
		if hostOnline(h) {
			return ""
		}
		if h.LastSeen.After(latest.LastSeen) {
			latest = h
		}
	}
	const fix = "; on that machine: systemctl status ephemera-host"
	seen := "was last seen " + lastSeen(latest)
	if latest.LastSeen.IsZero() {
		seen = "joined but has never connected"
	}
	if len(hosts) == 1 {
		return fmt.Sprintf("your only host, %s, is offline: it %s%s", latest.Name, seen, fix)
	}
	return fmt.Sprintf("none of your %d hosts is online; the most recent, %s, %s%s", len(hosts), latest.Name, seen, fix)
}

// explainNoHost replaces the central server's "no host" error with which
// hosts are offline and since when, when it can find out.
func explainNoHost(ctx context.Context, c *client.Client, err error) error {
	var se *client.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusServiceUnavailable || !strings.Contains(se.Msg, "hosts is connected") {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	hosts, herr := c.Hosts(ctx)
	if herr != nil {
		return err
	}
	if note := noHostNote(hosts); note != "" {
		return errors.New("no host can take a sandbox: " + note)
	}
	return err
}
