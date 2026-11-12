// Command ephemera-server is the central server: it tracks hosts and places
// sandboxes on them. It never receives files, command output or secrets.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	ver "github.com/pyjeebz/ephemera/internal/version"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/pgstore"
)

const usage = `ephemera-server is ephemera's central server.

Usage:
  ephemera-server [serve] [--listen ADDR] [--state DIR] [--database URL]
  ephemera-server token create <name> [--account NAME] [--max-sandboxes N] [--max-minutes N] [--state DIR]
  ephemera-server token limits <id-or-name> [--max-sandboxes N] [--max-minutes N] [--state DIR]
  ephemera-server token ls [--state DIR]
  ephemera-server token revoke <id-or-name> [--state DIR]
  ephemera-server version

Limits: --max-sandboxes caps sandboxes running at once, --max-minutes caps
sandbox-minutes per UTC day. 0 means no limit.

Accounts: a token sees and destroys only its own account's sandboxes.
Tokens made without --account share the account "local".

The state directory holds API tokens (tokens.json) and the list of running
sandboxes (sandboxes.json). With --database, or EPHEMERA_DATABASE_URL, they
are kept in that Postgres database instead (postgres://...), along with a
history of finished sandboxes; the token commands then use it too.
EPHEMERA_HOST_SECRET must be set for serve.

Web sign-in with GitHub needs the database and a GitHub OAuth app:
  EPHEMERA_GITHUB_CLIENT_ID, EPHEMERA_GITHUB_CLIENT_SECRET
  EPHEMERA_PUBLIC_URL      where browsers reach this server, e.g. https://api.example.com
  EPHEMERA_DASHBOARD_URL   where people land after signing in
The OAuth app's callback URL is EPHEMERA_PUBLIC_URL/auth/github/callback.
`

func defaultStateDir() string {
	if os.Geteuid() == 0 {
		return "/var/lib/ephemera/server"
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "ephemera", "server")
	}
	return "ephemera-state"
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// version is set at release time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintln(stdout, "ephemera-server", ver.String(version))
		return 0
	}
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
	database := fs.String("database", os.Getenv("EPHEMERA_DATABASE_URL"), "Postgres URL; instead of files in the state directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))

	srv := &control.Server{
		HostSecret: os.Getenv("EPHEMERA_HOST_SECRET"),
		Log:        log,
	}
	if srv.HostSecret == "" {
		log.Error("EPHEMERA_HOST_SECRET must be set")
		return 2
	}
	tokens, closeStore, err := openStores(*state, *database, srv)
	if err != nil {
		log.Error("open storage", "err", err)
		return 1
	}
	defer closeStore()
	if err := webSignIn(srv, *database); err != nil {
		log.Error("web sign-in", "err", err)
		return 2
	}
	if err := srv.Load(); err != nil {
		log.Error("load state", "err", err)
		return 1
	}
	if toks, _ := tokens.ListTokens(context.Background()); len(toks) == 0 {
		log.Warn("no API tokens yet; create one with: ephemera-server token create <name>")
	}
	where := "state " + *state
	if *database != "" {
		where = "Postgres"
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
	log.Info("central server listening", "addr", *listen, "storage", where, "version", ver.String(version))
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		return 1
	}
	return 0
}

// webSignIn turns on GitHub sign-in when its settings are in the
// environment. It needs Postgres, which keeps the sessions.
func webSignIn(srv *control.Server, database string) error {
	id := os.Getenv("EPHEMERA_GITHUB_CLIENT_ID")
	if id == "" {
		return nil
	}
	g := &control.GitHub{
		ClientID:     id,
		ClientSecret: os.Getenv("EPHEMERA_GITHUB_CLIENT_SECRET"),
		PublicURL:    os.Getenv("EPHEMERA_PUBLIC_URL"),
		DashboardURL: os.Getenv("EPHEMERA_DASHBOARD_URL"),
	}
	switch {
	case database == "":
		return errors.New("GitHub sign-in keeps sessions in Postgres; set EPHEMERA_DATABASE_URL too")
	case g.ClientSecret == "":
		return errors.New("EPHEMERA_GITHUB_CLIENT_SECRET is not set")
	case !strings.HasPrefix(g.PublicURL, "https://") && !strings.HasPrefix(g.PublicURL, "http://"):
		return errors.New("EPHEMERA_PUBLIC_URL must be this server's http(s) URL")
	case !strings.HasPrefix(g.DashboardURL, "https://") && !strings.HasPrefix(g.DashboardURL, "http://"):
		return errors.New("EPHEMERA_DASHBOARD_URL must be the dashboard's http(s) URL")
	}
	accounts, ok := srv.Tokens.(control.AccountStore)
	if !ok {
		return errors.New("this storage can't keep accounts")
	}
	srv.GitHub, srv.Accounts = g, accounts
	return nil
}

// openStores returns where tokens are kept, and sets srv's Tokens and State
// to match: Postgres when database is set, otherwise files in state. The
// returned func closes the database.
func openStores(state, database string, srv *control.Server) (control.TokenStore, func(), error) {
	if database != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		db, err := pgstore.Open(ctx, database)
		if err != nil {
			return nil, nil, err
		}
		if srv != nil {
			srv.Tokens, srv.State = db, db
		}
		return db, db.Close, nil
	}
	tf := &control.TokenFile{Path: filepath.Join(state, "tokens.json")}
	if srv != nil {
		srv.Tokens = tf
		srv.StatePath = filepath.Join(state, "sandboxes.json")
	}
	return tf, func() {}, nil
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
	database := fs.String("database", os.Getenv("EPHEMERA_DATABASE_URL"), "Postgres URL; instead of the state directory")
	var limits control.Limits
	fs.IntVar(&limits.MaxSandboxes, "max-sandboxes", 0, "sandboxes running at once (0: no limit)")
	fs.IntVar(&limits.MaxMinutesPerDay, "max-minutes", 0, "sandbox-minutes per UTC day (0: no limit)")
	account := fs.String("account", "", `account the token belongs to (create only; default "local")`)
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
	tf, closeStore, err := openStores(*state, *database, nil)
	if err != nil {
		fmt.Fprintln(stderr, "ephemera-server:", err)
		return 1
	}
	defer closeStore()
	ctx := context.Background()

	switch {
	case sub == "create" && len(pos) == 1:
		rec, secret, err := tf.CreateToken(ctx, pos[0], *account, limits)
		if err != nil {
			fmt.Fprintln(stderr, "ephemera-server:", err)
			return 1
		}
		fmt.Fprintf(stderr, "Created token %s (%s). It is shown only once:\n", rec.ID, rec.Name)
		fmt.Fprintln(stdout, secret)
		return 0
	case sub == "ls" && len(pos) == 0:
		recs, err := tf.ListTokens(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "ephemera-server:", err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tACCOUNT\tCREATED\tMAX SANDBOXES\tMAX MINUTES/DAY")
		limit := func(n int) string {
			if n == 0 {
				return "-"
			}
			return fmt.Sprint(n)
		}
		for _, r := range recs {
			acct := r.Account
			if acct == "" {
				acct = control.DefaultAccount
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, acct, r.Created.Format(time.RFC3339), limit(r.MaxSandboxes), limit(r.MaxMinutesPerDay))
		}
		tw.Flush()
		return 0
	case sub == "limits" && len(pos) == 1:
		if err := tf.SetTokenLimits(ctx, pos[0], limits); err != nil {
			fmt.Fprintln(stderr, "ephemera-server:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Limits for %s: %d sandboxes at once, %d minutes a day (0: no limit)\n", pos[0], limits.MaxSandboxes, limits.MaxMinutesPerDay)
		return 0
	case sub == "revoke" && len(pos) == 1:
		if err := tf.RevokeToken(ctx, pos[0]); err != nil {
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
