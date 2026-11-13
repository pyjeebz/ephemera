package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/config"
)

// doctorTimeout bounds each network check, so a dropped packet shows up as
// a clear failure instead of a hang.
const doctorTimeout = 5 * time.Second

// hostTimeout matches control.HostTimeout: a host with no heartbeat for
// this long gets no new sandboxes.
const hostTimeout = 45 * time.Second

// check is one line of the doctor's report.
type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail or skip
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// cmdDoctor checks, in order, everything the CLI needs to run a sandbox:
// a login, a reachable central server, a token it accepts, and an online
// host this machine can reach. A failed check skips the ones after it that
// depend on it.
func cmdDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print the checks as JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: ephemera doctor [--json]\n\n"+
			"Checks the login, the central server, the token and the hosts, and\n"+
			"says how to fix the first thing that fails.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	checks := runDoctor(ctx)
	ok := true
	for _, c := range checks {
		if c.Status == "fail" {
			ok = false
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(struct {
			OK     bool    `json:"ok"`
			Checks []check `json:"checks"`
		}{ok, checks})
	} else {
		for _, c := range checks {
			fmt.Fprintf(stdout, "%-4s  %-6s  %s\n", c.Status, c.Name, c.Detail)
			if c.Fix != "" {
				fmt.Fprintf(stdout, "              %s\n", c.Fix)
			}
		}
	}
	if !ok {
		return 1
	}
	return 0
}

func runDoctor(ctx context.Context) []check {
	var out []check
	skip := func(names ...string) {
		for _, n := range names {
			out = append(out, check{Name: n, Status: "skip", Detail: "skipped: an earlier check failed"})
		}
	}

	// 1. A server address and a token to send it.
	login, server, token := checkLogin()
	out = append(out, login)
	if login.Status == "fail" {
		skip("server", "token", "hosts")
		return out
	}

	// 2. The central server answers.
	srv := checkReach(ctx, http.DefaultClient, "server", server, "the central server")
	if srv.Status == "fail" {
		out = append(out, srv)
		skip("token", "hosts")
		return out
	}
	out = append(out, srv)

	// 3. The server accepts the token.
	c := &client.Client{Server: strings.TrimRight(server, "/"), Token: token}
	// The doctor reads the pins but never adds one.
	if pins, err := loadPins(); err == nil {
		c.Pins = pins
	}
	tctx, cancel := context.WithTimeout(ctx, doctorTimeout)
	who, err := c.WhoAmI(tctx)
	cancel()
	if err != nil {
		tok := check{Name: "token", Status: "fail", Detail: "the server did not accept the token: " + err.Error()}
		var se *client.StatusError
		if errors.As(err, &se) && se.Code == http.StatusUnauthorized {
			tok.Detail = "the server rejected the token; it may have been revoked or mistyped"
			tok.Fix = "Sign in again: ephemera login. For CI, make a new token in the dashboard, or on your own server with: ephemera-server token create <name>"
		}
		out = append(out, tok)
		skip("hosts")
		return out
	}
	out = append(out, check{Name: "token", Status: "ok", Detail: "accepted, name " + who.Name})

	// 4. A host is online, and this machine can reach it: files and
	// commands go straight to the host, not through the server.
	out = append(out, checkHosts(ctx, c)...)
	return out
}

// checkLogin reports where the server address and token come from.
func checkLogin() (check, string, string) {
	saved, err := config.Load()
	if err != nil {
		return check{Name: "login", Status: "fail", Detail: "can't read the saved login: " + err.Error(),
			Fix: "Fix or remove that file, then run: ephemera login --server <URL>"}, "", ""
	}
	path, _ := config.Path()
	server, serverFrom := saved.Server, "saved login"
	if v := os.Getenv("EPHEMERA_SERVER"); v != "" {
		server, serverFrom = v, "EPHEMERA_SERVER"
	}
	token, tokenFrom := saved.Token, "saved login"
	if v := os.Getenv("EPHEMERA_TOKEN"); v != "" {
		token, tokenFrom = v, "EPHEMERA_TOKEN"
	}
	if token == "" {
		return check{Name: "login", Status: "fail", Detail: "not logged in: no token in " + path + " or EPHEMERA_TOKEN",
			Fix: "Run: ephemera login (add --server <URL> for a central server you run)"}, "", ""
	}
	if server == "" {
		server, serverFrom = config.DefaultServer, "the default"
	}
	return check{Name: "login", Status: "ok",
		Detail: fmt.Sprintf("server %s (from %s), token from %s", server, serverFrom, tokenFrom)}, server, token
}

// checkReach asks an ephemera server or host for /healthz, which needs no
// token, and explains a failure in network terms.
func checkReach(ctx context.Context, hc *http.Client, name, base, what string) check {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return check{Name: name, Status: "fail", Detail: fmt.Sprintf("%q is not an http(s) URL", base),
			Fix: "Run: ephemera login, or ephemera login --server <URL> for a central server you run"}
	}
	ctx, cancel := context.WithTimeout(ctx, doctorTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/healthz", nil)
	start := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		detail, fix := explainNetErr(u, what, err)
		return check{Name: name, Status: "fail", Detail: detail, Fix: fix}
	}
	resp.Body.Close()
	took := time.Since(start).Round(time.Millisecond)
	if resp.StatusCode != http.StatusOK {
		return check{Name: name, Status: "fail",
			Detail: fmt.Sprintf("%s answered %s, not ok", base, resp.Status),
			Fix:    "Check that this is the address of " + what + ", not another service"}
	}
	return check{Name: name, Status: "ok", Detail: fmt.Sprintf("%s answers in %s", base, took)}
}

// explainNetErr turns a failed request into what went wrong and what to try.
func explainNetErr(u *url.URL, what string, err error) (string, string) {
	addr := u.Host
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr):
		return fmt.Sprintf("can't find %s: the name does not resolve", u.Hostname()),
			"Check the address. On Tailscale, check this machine is on the same tailnet: tailscale status"
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Sprintf("nothing is listening at %s", addr),
			"Check that " + what + " is running there, and on that port"
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return fmt.Sprintf("no answer from %s within %s", addr, doctorTimeout),
			"Check the address, that the machine is on, and that this machine can reach it (same network or tailnet, firewall)"
	case errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH):
		return fmt.Sprintf("no route to %s", addr),
			"This machine has no network path to it. On Tailscale, check: tailscale status"
	case strings.Contains(err.Error(), "server gave HTTP response to HTTPS client"):
		return fmt.Sprintf("%s speaks plain HTTP, not HTTPS", addr),
			"Use http:// in the address: ephemera login --server http://" + addr
	case errors.As(err, &opErr):
		return fmt.Sprintf("can't connect to %s: %v", addr, opErr.Err), "Check the address and the network"
	}
	return fmt.Sprintf("request to %s failed: %v", addr, err), "Check the address and the network"
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// checkHosts reports whether any host is online and reachable from here.
func checkHosts(ctx context.Context, c *client.Client) []check {
	hctx, cancel := context.WithTimeout(ctx, doctorTimeout)
	hosts, err := c.Hosts(hctx)
	cancel()
	if err != nil {
		return []check{{Name: "hosts", Status: "fail", Detail: "can't list hosts: " + err.Error()}}
	}
	if len(hosts) == 0 {
		return []check{{Name: "hosts", Status: "fail", Detail: "no host has registered with this server",
			Fix: "Add a host: make a join code in the dashboard, under Hosts, and on a Linux machine with KVM run: curl -fsSL https://ephemera.run/host | sudo sh -s -- <code>. See https://ephemera.run/docs/guides/hosts"}}
	}
	var out []check
	reachable := false
	for _, h := range hosts {
		if h.URL == "" {
			// A server older than this CLI sends neither url nor online, so
			// judge by the last heartbeat on this machine's clock.
			h.Online = time.Since(h.LastSeen) <= hostTimeout
		}
		if !h.Online {
			detail := fmt.Sprintf("%s is offline, last seen %s ago", h.Name, time.Since(h.LastSeen).Round(time.Second))
			if h.LastSeen.IsZero() {
				detail = h.Name + " joined but hasn't connected since the central server started"
			}
			out = append(out, check{Name: "hosts", Status: "fail",
				Detail: detail,
				Fix:    "On that machine, check the host agent: systemctl status ephemera-host"})
			continue
		}
		if h.URL == "" {
			reachable = true
			out = append(out, check{Name: "hosts", Status: "ok", Detail: h.Name + " is online"})
			continue
		}
		hc, err := c.HostClient(h)
		if err != nil {
			fail := check{Name: "hosts", Status: "fail", Detail: fmt.Sprintf("%s is online, but %v", h.Name, err)}
			if keyChanged(err) {
				fail.Detail = err.Error()
				fail.Fix = "If you rebuilt or re-joined it, accept the new key: ephemera trust " + h.Name
			}
			out = append(out, fail)
			continue
		}
		r := checkReach(ctx, hc, "hosts", h.URL, "the host agent")
		if r.Status == "ok" {
			reachable = true
			pinned := "its key isn't pinned yet; the first run pins it"
			if c.Pins != nil {
				if _, ok := c.Pins.Get(h.ID); ok {
					pinned = "its key is pinned"
				}
			}
			r.Detail = fmt.Sprintf("%s is online, %s, and %s", h.Name, pinned, r.Detail)
		} else {
			r.Detail = fmt.Sprintf("%s is online, but this machine can't reach it: %s", h.Name, r.Detail)
			r.Fix = "Files and commands go straight to the host, so this machine must reach " + h.URL + ". " + r.Fix
		}
		out = append(out, r)
	}
	// One reachable host is enough to run sandboxes; the others are only
	// worth a look.
	if reachable {
		for i := range out {
			if out[i].Status == "fail" {
				out[i].Status = "warn"
			}
		}
	}
	return out
}
