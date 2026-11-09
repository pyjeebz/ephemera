// Command ephemera-server is the central server: it tracks hosts and places
// sandboxes on them. It never receives files, command output or secrets.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/pyjeebz/ephemera/internal/control"
)

const usage = `ephemera-server is ephemera's central server.

Usage:
  ephemera-server [serve] [--listen ADDR] [--state DIR]
  ephemera-server token create <name> [--max-sandboxes N] [--max-minutes N] [--state DIR]
  ephemera-server token limits <id-or-name> [--max-sandboxes N] [--max-minutes N] [--state DIR]
  ephemera-server token ls [--state DIR]
  ephemera-server token revoke <id-or-name> [--state DIR]

Limits: --max-sandboxes caps sandboxes running at once, --max-minutes caps
sandbox-minutes per UTC day. 0 means no limit.

The state directory holds API tokens (tokens.json) and the list of running
sandboxes (sandboxes.json). EPHEMERA_HOST_SECRET must be set for serve.
`

func defaultStateDir() string {
	if os.Geteuid() == 0 {
		return "/var/lib/ephemera"
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "ephemera", "server")
	}
	return "ephemera-state"
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "token" {
		return tokenCmd(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, usage)
		return 0
	}
	return serve(args, stderr)
}

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:7700", "address to listen on")
	state := fs.String("state", defaultStateDir(), "state directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))

	srv := &control.Server{
		Tokens:     &control.TokenFile{Path: filepath.Join(*state, "tokens.json")},
		HostSecret: os.Getenv("EPHEMERA_HOST_SECRET"),
		StatePath:  filepath.Join(*state, "sandboxes.json"),
		Client:     &http.Client{Timeout: 2 * time.Minute},
		Log:        log,
	}
	if srv.HostSecret == "" {
		log.Error("EPHEMERA_HOST_SECRET must be set")
		return 2
	}
	if err := srv.Load(); err != nil {
		log.Error("load state", "err", err)
		return 1
	}
	if toks, _ := srv.Tokens.(*control.TokenFile).List(); len(toks) == 0 {
		log.Warn("no API tokens yet; create one with: ephemera-server token create <name>", "state", *state)
	}

	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	go srv.Enforce(ctx, 15*time.Second)
	log.Info("central server listening", "addr", *listen, "state", *state)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		return 1
	}
	return 0
}

func tokenCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("token "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", defaultStateDir(), "state directory")
	var limits control.Limits
	fs.IntVar(&limits.MaxSandboxes, "max-sandboxes", 0, "sandboxes running at once (0: no limit)")
	fs.IntVar(&limits.MaxMinutesPerDay, "max-minutes", 0, "sandbox-minutes per UTC day (0: no limit)")
	// Allow the flag before or after the positional argument.
	var pos []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	tf := &control.TokenFile{Path: filepath.Join(*state, "tokens.json")}

	switch {
	case sub == "create" && len(pos) == 1:
		rec, secret, err := tf.Create(pos[0], limits)
		if err != nil {
			fmt.Fprintln(stderr, "ephemera-server:", err)
			return 1
		}
		fmt.Fprintf(stderr, "Created token %s (%s). It is shown only once:\n", rec.ID, rec.Name)
		fmt.Fprintln(stdout, secret)
		return 0
	case sub == "ls" && len(pos) == 0:
		recs, err := tf.List()
		if err != nil {
			fmt.Fprintln(stderr, "ephemera-server:", err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tCREATED\tMAX SANDBOXES\tMAX MINUTES/DAY")
		limit := func(n int) string {
			if n == 0 {
				return "-"
			}
			return fmt.Sprint(n)
		}
		for _, r := range recs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Created.Format(time.RFC3339), limit(r.MaxSandboxes), limit(r.MaxMinutesPerDay))
		}
		tw.Flush()
		return 0
	case sub == "limits" && len(pos) == 1:
		if err := tf.SetLimits(pos[0], limits); err != nil {
			fmt.Fprintln(stderr, "ephemera-server:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Limits for %s: %d sandboxes at once, %d minutes a day (0: no limit)\n", pos[0], limits.MaxSandboxes, limits.MaxMinutesPerDay)
		return 0
	case sub == "revoke" && len(pos) == 1:
		if err := tf.Revoke(pos[0]); err != nil {
			fmt.Fprintln(stderr, "ephemera-server:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Revoked %s\n", pos[0])
		return 0
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}
