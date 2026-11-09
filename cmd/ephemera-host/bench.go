package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/pyjeebz/ephemera/internal/bench"
)

const benchUsage = `Usage: ephemera-host bench [flags]

Measures this host with the Firecracker backend: cold boot, snapshot
restore, ephemera run end to end, output latency, setup snapshots and,
with --density, many sandboxes at once. Prints a table and writes the
results as JSON. Run it as root, with no other benchmark running.

Flags:
`

// benchMain runs `ephemera-host bench`.
func benchMain(args []string) int {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, benchUsage); fs.PrintDefaults() }
	imageDir := fs.String("image-dir", "/var/lib/ephemera/image", "guest image from deploy/guest/build-image.sh")
	jail := fs.Bool("jail", os.Geteuid() == 0, "run each microVM under the jailer, as production hosts do")
	runs := fs.Int("runs", 10, "samples per timing")
	density := fs.Int("density", 20, "sandboxes started at once (0 skips the density test)")
	memory := fs.Int("memory", 1024, "guest memory per sandbox, in MB")
	cpus := fs.Int("cpus", 2, "vCPUs per sandbox")
	out := fs.String("out", "", "where to write the JSON (default bench-<commit>-<time>.json)")
	compare := fs.String("compare", "", "an earlier JSON report to compare with")
	commit := fs.String("commit", buildCommit(), "commit being measured")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var prev *bench.Report
	if *compare != "" {
		var err error
		if prev, err = bench.Load(*compare); err != nil {
			fmt.Fprintln(os.Stderr, "ephemera-host bench:", err)
			return 2
		}
	}
	if *jail && os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "ephemera-host bench: the jailer needs root; run as root or pass --jail=false")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := bench.Config{ImageDir: *imageDir, Jailed: *jail, Runs: *runs, Density: *density, MemoryMB: *memory, CPUs: *cpus,
		// Beside the image, so jails can hard-link it.
		WorkDir: filepath.Join(filepath.Dir(*imageDir), fmt.Sprintf("bench-%d", os.Getpid()))}
	fmt.Fprintln(os.Stderr, "measuring; this takes a few minutes")
	r, err := bench.Run(ctx, cfg, func(res bench.Result) { fmt.Fprintf(os.Stderr, "  done: %s\n", res.Name) })
	if r != nil {
		r.Commit = *commit
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ephemera-host bench:", err)
		return 1
	}
	path := *out
	if path == "" {
		path = fmt.Sprintf("bench-%s-%s.json", shortCommit(r.Commit), r.Date.Format("20060102-150405"))
	}
	if err := bench.Save(r, path); err != nil {
		fmt.Fprintln(os.Stderr, "ephemera-host bench:", err)
		return 1
	}
	fmt.Println()
	bench.Print(os.Stdout, r, prev)
	fmt.Printf("\nresults saved to %s\n", path)
	return 0
}

// buildCommit is the commit the binary was built from, when Go recorded it.
func buildCommit() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		var rev, dirty string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "-dirty"
				}
			}
		}
		if rev != "" {
			return rev + dirty
		}
	}
	return "unknown"
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
