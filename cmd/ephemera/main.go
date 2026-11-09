// Command ephemera is the CLI: it runs commands in short-lived sandboxes
// while you keep editing locally.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/config"
)

var version = "dev"

// exitFailure is returned when ephemera itself fails, as opposed to the
// command it ran. 125 follows the convention of docker run.
const exitFailure = 125

const usage = `ephemera runs commands in short-lived, locked-down sandboxes.

Usage:
  ephemera run [flags] -- <command> [args...]
  ephemera dev [flags] [-p PORT]... -- <dev server command>
  ephemera routes                   dev sessions sharing ports, and their addresses
  ephemera focus <session>          make plain localhost show that session
  ephemera ls [--json]
  ephemera rm <sandbox-id>...
  ephemera verify <record.json>
  ephemera mcp [--dir DIR]          serve ephemera to a coding agent over MCP
  ephemera login [--server URL]
  ephemera logout
  ephemera version

Environment (overrides the saved login):
  EPHEMERA_SERVER   central server URL (default http://127.0.0.1:7700)
  EPHEMERA_TOKEN    API token
  EPHEMERA_CONFIG   where the login is saved

Run "ephemera run -h" or "ephemera dev -h" for their flags.
`

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func realMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server, token, err := config.Resolve()
	if err != nil {
		fmt.Fprintln(stderr, "ephemera: reading saved login:", err)
	}
	c := &client.Client{Server: server, Token: token}
	switch args[0] {
	case "router":
		return cmdRouter(ctx, stderr)
	case "routes":
		return cmdRoutes(ctx, stdout, stderr)
	case "focus":
		return cmdFocus(ctx, args[1:], stdout, stderr)
	case "mcp":
		return cmdMCP(ctx, c, args[1:], stdin, stdout, stderr)
	case "verify":
		return cmdVerify(ctx, c, args[1:], stdout, stderr)
	case "login":
		return cmdLogin(ctx, args[1:], stdin, stdout, stderr)
	case "logout":
		return cmdLogout(stdout, stderr)
	case "run":
		return cmdRun(ctx, c, args[1:], stdout, stderr)
	case "dev":
		return cmdDev(ctx, c, args[1:], stdout, stderr)
	case "ls":
		return cmdLs(ctx, c, args[1:], stdout, stderr)
	case "rm":
		return cmdRm(ctx, c, args[1:], stderr)
	case "version", "--version":
		fmt.Fprintln(stdout, "ephemera", version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "ephemera: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// sandboxFlags are the flags run and dev share.
type sandboxFlags struct {
	spec    api.Spec
	include stringList
	env     stringList
	allow   stringList
	dir     string
	jsonOut bool
	openNet bool
	timeout time.Duration
	record  string
}

func (f *sandboxFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.spec.Image, "image", "", "sandbox image (default: the host's default)")
	fs.Float64Var(&f.spec.CPUs, "cpus", 0, "CPU limit, e.g. 2 or 0.5 (default: no limit)")
	fs.IntVar(&f.spec.MemoryMB, "memory", 0, "memory limit in MB (default: no limit)")
	fs.BoolVar(&f.openNet, "net", false, "allow outbound access to any public host (blocked by default; still logged)")
	fs.Var(&f.allow, "allow", "allow outbound access to a host: example.com, *.example.com, host:port (repeatable)")
	fs.Var(&f.include, "include", "send an ignored file anyway, e.g. .env.local (repeatable)")
	fs.Var(&f.env, "env", "set an environment variable, KEY=VALUE (repeatable)")
	fs.StringVar(&f.dir, "dir", ".", "worktree to sync")
	fs.BoolVar(&f.jsonOut, "json", false, "print newline-delimited JSON events instead of raw output")
	fs.DurationVar(&f.timeout, "timeout", 0, "run: stop the command after this long (exit code 124); dev: destroy the sandbox after this long. e.g. 10m")
	fs.StringVar(&f.record, "record", "", "save the sandbox's signed record here (default: the records directory)")
}

func (f *sandboxFlags) check() error {
	if f.openNet && len(f.allow) > 0 {
		return fmt.Errorf("use either --net (any host) or --allow (listed hosts), not both")
	}
	if f.openNet {
		f.spec.Network = api.NetworkOpen
	}
	if len(f.allow) > 0 {
		f.spec.Network = api.NetworkAllow
		f.spec.Allow = f.allow
	}
	if f.timeout < 0 {
		return fmt.Errorf("--timeout cannot be negative")
	}
	for _, kv := range f.env {
		if !strings.Contains(kv, "=") {
			return fmt.Errorf("--env %q must be KEY=VALUE", kv)
		}
	}
	return nil
}

// jsonEvents writes JSON events to w, one goroutine at a time.
type jsonEvents struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (j *jsonEvents) emit(ev string, f map[string]any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f["event"] = ev
	j.enc.Encode(f)
}

func parseCommand(name string, fs *flag.FlagSet, args []string, stderr io.Writer) ([]string, bool) {
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: ephemera %s [flags] -- <command> [args...]\n\nFlags:\n", name)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, false
	}
	if len(fs.Args()) == 0 {
		fs.Usage()
		return nil, false
	}
	return fs.Args(), true
}

func cmdRun(ctx context.Context, c *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f sandboxFlags
	f.register(fs)
	argv, ok := parseCommand("run", fs, args, stderr)
	if !ok {
		return 2
	}
	if err := f.check(); err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return 2
	}

	opts := client.RunOptions{
		Dir:     f.dir,
		Include: f.include,
		Spec:    f.spec,
		Exec:    api.ExecRequest{Argv: argv, Env: f.env},
		Stdout:  stdout,
		Stderr:  stderr,
	}
	if f.timeout > 0 {
		opts.Exec.TimeoutSeconds = int((f.timeout + time.Second - 1) / time.Second)
		// The sandbox itself goes soon after, even if the CLI does not.
		opts.Spec.MaxLifetimeSeconds = opts.Exec.TimeoutSeconds + 300
	}
	events := &jsonEvents{enc: json.NewEncoder(stdout)}
	if f.jsonOut {
		opts.Events = events.emit
		opts.Stdout = eventWriter{emit: events.emit, stream: "stdout"}
		opts.Stderr = eventWriter{emit: events.emit, stream: "stderr"}
	}

	res, err := c.Run(ctx, opts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			err = errors.New("interrupted; the sandbox was destroyed")
		}
		if f.jsonOut {
			events.emit("error", map[string]any{"error": err.Error()})
		} else {
			fmt.Fprintln(stderr, "ephemera:", err)
		}
		return exitFailure
	}
	recPath := ""
	if res.Record != nil {
		if p, err := saveRecord(res.Record, f.record); err != nil {
			fmt.Fprintln(stderr, "ephemera: saving the record:", err)
		} else {
			recPath = p
		}
	}
	if f.jsonOut {
		ev := map[string]any{"code": res.ExitCode, "duration_ms": res.Duration.Milliseconds(), "sandbox": res.SandboxID}
		if recPath != "" {
			ev["record"] = recPath
		}
		events.emit("exit", ev)
	} else {
		if len(res.Connections) > 0 {
			printNetworkSummary(stderr, res.Connections)
		}
		if recPath != "" {
			fmt.Fprintf(stderr, "ephemera: signed record saved to %s\n", recPath)
		}
	}
	return res.ExitCode
}

func plural(n any, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%v %ss", n, word)
}

// exitInterrupted is returned when ephemera dev is stopped with Ctrl-C.
const exitInterrupted = 130

func cmdDev(ctx context.Context, c *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f sandboxFlags
	f.register(fs)
	var ports stringList
	fs.Var(&ports, "p", "forward a port: PORT is shared by session at http://<name>.localhost:PORT; LOCAL:SANDBOX binds LOCAL for this session only (repeatable)")
	name := fs.String("name", "", "session name for shared ports (default: the git branch)")
	argv, ok := parseCommand("dev", fs, args, stderr)
	if !ok {
		return 2
	}
	if err := f.check(); err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return 2
	}
	opts := client.DevOptions{
		Dir:     f.dir,
		Include: f.include,
		Spec:    f.spec,
		Exec:    api.ExecRequest{Argv: argv, Env: f.env},
		Stdout:  stdout,
		Stderr:  stderr,
	}
	if f.timeout > 0 {
		opts.Spec.MaxLifetimeSeconds = int((f.timeout + time.Second - 1) / time.Second)
	}
	recPath := ""
	opts.OnRecord = func(sr *api.SignedRecord) {
		if p, err := saveRecord(sr, f.record); err == nil {
			recPath = p
		} else {
			fmt.Fprintln(stderr, "ephemera: saving the record:", err)
		}
	}
	for _, p := range ports {
		pm, err := client.ParsePort(p)
		if err != nil {
			fmt.Fprintln(stderr, "ephemera:", err)
			return 2
		}
		opts.Ports = append(opts.Ports, pm)
	}
	if rc, err := routerClient(); err == nil {
		opts.Router = rc
		opts.StartRouter = func() error { return startRouter(rc) }
	}
	opts.Name = *name
	if opts.Name == "" {
		opts.Name = sessionName(f.dir)
	}

	events := &jsonEvents{enc: json.NewEncoder(stdout)}
	if f.jsonOut {
		opts.Events = events.emit
		opts.Stdout = eventWriter{emit: events.emit, stream: "stdout"}
		opts.Stderr = eventWriter{emit: events.emit, stream: "stderr"}
	} else {
		opts.Events = func(ev string, m map[string]any) {
			switch ev {
			case "sandbox":
				fmt.Fprintf(stderr, "ephemera: sandbox %s on %s\n", m["id"], m["host"])
			case "synced":
				if m["initial"] == true {
					fmt.Fprintf(stderr, "ephemera: synced %s; watching for changes\n", plural(m["files"], "file"))
				} else {
					fmt.Fprintf(stderr, "ephemera: synced %d changed, %d deleted\n", m["files"], m["deleted"])
				}
			case "forwarding":
				fmt.Fprintf(stderr, "ephemera: http://%s -> sandbox port %d\n", m["local"], m["port"])
				if m["focused"] == true {
					fmt.Fprintf(stderr, "ephemera: http://%s shows this session too (ephemera focus <name> to switch)\n", m["bare"])
				}
			case "connection":
				if m["allowed"] != true {
					fmt.Fprintf(stderr, "ephemera: blocked %s:%v (%v)\n", m["host"], m["port"], m["error"])
				}
			case "sync_error", "port_error":
				fmt.Fprintf(stderr, "ephemera: %s\n", m["error"])
			case "destroyed":
				fmt.Fprintf(stderr, "ephemera: sandbox %s destroyed\n", m["id"])
				if recPath != "" {
					fmt.Fprintf(stderr, "ephemera: signed record saved to %s\n", recPath)
				}
			}
		}
	}

	code, err := c.Dev(ctx, opts)
	if errors.Is(err, context.Canceled) {
		if f.jsonOut {
			events.emit("stopped", map[string]any{})
		}
		return exitInterrupted
	}
	if err != nil {
		if f.jsonOut {
			events.emit("error", map[string]any{"error": err.Error()})
		} else {
			fmt.Fprintln(stderr, "ephemera:", err)
		}
		return exitFailure
	}
	if f.jsonOut {
		events.emit("exit", map[string]any{"code": code})
	} else {
		fmt.Fprintf(stderr, "ephemera: dev command exited with code %d\n", code)
	}
	return code
}

// printNetworkSummary reports the sandbox's outbound connections: a count,
// and every blocked one by name.
func printNetworkSummary(w io.Writer, conns []api.Connection) {
	allowed := 0
	var blocked []api.Connection
	for _, c := range conns {
		if c.Allowed {
			allowed++
		} else {
			blocked = append(blocked, c)
		}
	}
	fmt.Fprintf(w, "ephemera: network: %d allowed, %d blocked\n", allowed, len(blocked))
	seen := map[string]bool{}
	for _, c := range blocked {
		key := fmt.Sprintf("%s:%d", c.Host, c.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		fmt.Fprintf(w, "ephemera:   blocked %s (%s)\n", key, c.Error)
	}
}

// eventWriter turns command output into JSON events.
type eventWriter struct {
	emit   func(string, map[string]any)
	stream string
}

func (w eventWriter) Write(p []byte) (int, error) {
	w.emit("output", map[string]any{"stream": w.stream, "data": string(p)})
	return len(p), nil
}

func cmdLs(ctx context.Context, c *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	list, err := c.List(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
	}
	if *jsonOut {
		json.NewEncoder(stdout).Encode(list)
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tHOST\tNETWORK\tAGE")
	for _, sb := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", sb.ID, sb.Host, sb.Spec.Network, time.Since(sb.CreatedAt).Round(time.Second))
	}
	tw.Flush()
	return 0
}

func cmdRm(ctx context.Context, c *client.Client, ids []string, stderr io.Writer) int {
	if len(ids) == 0 {
		fmt.Fprintln(stderr, "Usage: ephemera rm <sandbox-id>...")
		return 2
	}
	code := 0
	for _, id := range ids {
		if _, err := c.Destroy(ctx, id); err != nil {
			fmt.Fprintln(stderr, "ephemera:", err)
			code = exitFailure
		}
	}
	return code
}
