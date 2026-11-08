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
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
)

var version = "dev"

// exitFailure is returned when ephemera itself fails, as opposed to the
// command it ran. 125 follows the convention of docker run.
const exitFailure = 125

const usage = `ephemera runs commands in short-lived, locked-down sandboxes.

Usage:
  ephemera run [flags] -- <command> [args...]
  ephemera ls [--json]
  ephemera rm <sandbox-id>...
  ephemera version

Environment:
  EPHEMERA_SERVER   central server URL (default http://127.0.0.1:7700)
  EPHEMERA_TOKEN    API token

Run "ephemera run -h" for run's flags.
`

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

func realMain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := &client.Client{
		Server: envOr("EPHEMERA_SERVER", "http://127.0.0.1:7700"),
		Token:  os.Getenv("EPHEMERA_TOKEN"),
	}
	switch args[0] {
	case "run":
		return cmdRun(ctx, c, args[1:], stdout, stderr)
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

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func cmdRun(ctx context.Context, c *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		spec    api.Spec
		include stringList
		env     stringList
		dir     string
		jsonOut bool
		openNet bool
	)
	fs.StringVar(&spec.Image, "image", "", "sandbox image (default: the host's default)")
	fs.Float64Var(&spec.CPUs, "cpus", 0, "CPU limit, e.g. 2 or 0.5 (default: no limit)")
	fs.IntVar(&spec.MemoryMB, "memory", 0, "memory limit in MB (default: no limit)")
	fs.BoolVar(&openNet, "net", false, "allow outbound network access (blocked by default)")
	fs.Var(&include, "include", "send an ignored file anyway, e.g. .env.local (repeatable)")
	fs.Var(&env, "env", "set an environment variable, KEY=VALUE (repeatable)")
	fs.StringVar(&dir, "dir", ".", "worktree to sync")
	fs.BoolVar(&jsonOut, "json", false, "print newline-delimited JSON events instead of raw output")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: ephemera run [flags] -- <command> [args...]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	argv := fs.Args()
	if len(argv) == 0 {
		fs.Usage()
		return 2
	}
	if openNet {
		spec.Network = api.NetworkOpen
	}
	for _, kv := range env {
		if !strings.Contains(kv, "=") {
			fmt.Fprintf(stderr, "ephemera: --env %q must be KEY=VALUE\n", kv)
			return 2
		}
	}

	opts := client.RunOptions{
		Dir:     dir,
		Include: include,
		Spec:    spec,
		Exec:    api.ExecRequest{Argv: argv, Env: env},
		Stdout:  stdout,
		Stderr:  stderr,
	}
	var events *json.Encoder
	if jsonOut {
		events = json.NewEncoder(stdout)
		emit := func(ev string, f map[string]any) {
			f["event"] = ev
			events.Encode(f)
		}
		opts.Events = emit
		opts.Stdout = eventWriter{emit: emit, stream: "stdout"}
		opts.Stderr = eventWriter{emit: emit, stream: "stderr"}
	}

	res, err := c.Run(ctx, opts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			err = errors.New("interrupted; the sandbox was destroyed")
		}
		if jsonOut {
			events.Encode(map[string]any{"event": "error", "error": err.Error()})
		} else {
			fmt.Fprintln(stderr, "ephemera:", err)
		}
		return exitFailure
	}
	if jsonOut {
		events.Encode(map[string]any{"event": "exit", "code": res.ExitCode,
			"duration_ms": res.Duration.Milliseconds(), "sandbox": res.SandboxID})
	}
	return res.ExitCode
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
		if err := c.Destroy(ctx, id); err != nil {
			fmt.Fprintln(stderr, "ephemera:", err)
			code = exitFailure
		}
	}
	return code
}
