// Command eph drives ephemera machines.
//
// Phase 1 talks to the machine package directly so the VM path can be exercised
// before ephemerad exists; later it becomes a client of the daemon's API.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/machine"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	var err error
	switch args[0] {
	case "boot":
		err = cmdBoot(args[1:])
	case "run":
		err = cmdRun(args[1:])
	case "create":
		err = cmdCreate(args[1:])
	case "ls":
		err = cmdList(args[1:])
	case "exec":
		err = cmdExec(args[1:])
	case "rm":
		err = cmdRm(args[1:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "eph: unknown command %q\n\n", args[0])
		usage()
		os.Exit(2)
	}

	if err != nil {
		// A command that ran and failed reports the guest's status, so scripts
		// can tell "the sandbox broke" from "the command exited 1".
		var ge guestExit
		if ok := asGuestExit(err, &ge); ok {
			os.Exit(ge.code)
		}
		fmt.Fprintln(os.Stderr, "eph:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: eph <command> [flags]

standalone — drive a machine directly, no daemon needed:
  boot     boot a machine and stream its serial console
  run      boot a machine, run one command inside it, then destroy it

managed — talk to ephemerad, machines outlive the command:
  create   boot a machine and leave it running
  ls       list running machines
  exec     run a command in an existing machine
  rm       destroy a machine

run "eph <command> -h" for flags
`)
}

// common holds the flags every command shares.
type common struct {
	kernel *string
	rootfs *string
	vcpus  *int
	mem    *int
	runDir *string
}

func addCommon(fs *flag.FlagSet) *common {
	return &common{
		kernel: fs.String("kernel", "build/kernel/vmlinux", "guest kernel (uncompressed ELF vmlinux)"),
		rootfs: fs.String("rootfs", "build/rootfs/rootfs.ext4", "guest root filesystem image"),
		vcpus:  fs.Int("cpus", machine.DefaultVCPUs, "vCPU count"),
		mem:    fs.Int("mem", machine.DefaultMemMiB, "memory in MiB"),
		runDir: fs.String("run-dir", "run", "directory for per-machine runtime state"),
	}
}

// config resolves the shared flags into a machine config.
//
// Firecracker opens the kernel and rootfs itself, so the paths are made
// absolute here — a bad path then fails locally with context instead of coming
// back as a fault message from the VMM.
func (c *common) config() (machine.Config, error) {
	kernelPath, err := filepath.Abs(*c.kernel)
	if err != nil {
		return machine.Config{}, err
	}
	if resolved, err := filepath.EvalSymlinks(kernelPath); err == nil {
		kernelPath = resolved
	}
	rootfsPath, err := filepath.Abs(*c.rootfs)
	if err != nil {
		return machine.Config{}, err
	}
	return machine.Config{
		KernelPath: kernelPath,
		RootfsPath: rootfsPath,
		VCPUs:      *c.vcpus,
		MemMiB:     *c.mem,
		RunDir:     *c.runDir,
	}, nil
}

func cmdBoot(argv []string) error {
	fs := flag.NewFlagSet("boot", flag.ExitOnError)
	cf := addCommon(fs)
	init := fs.String("init", machine.DefaultInit, "guest init; /sbin/eph-selftest runs a check and halts")
	timeout := fs.Duration("timeout", 0, "destroy the machine after this long (0 = no limit)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: eph boot [flags]\n\nBoots a machine and streams its console until it exits.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}

	cfg, err := cf.config()
	if err != nil {
		return err
	}
	cfg.Init = *init
	cfg.Console = os.Stdout
	cfg.ConsoleIn = os.Stdin

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	m, err := machine.Boot(ctx, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "eph: machine %s booted in %s\n", m.ID, since(started))

	var deadline <-chan time.Time
	if *timeout > 0 {
		t := time.NewTimer(*timeout)
		defer t.Stop()
		deadline = t.C
	}

	select {
	case <-m.Done():
		err = m.Wait()
	case <-deadline:
		fmt.Fprintf(os.Stderr, "eph: timeout reached, destroying %s\n", m.ID)
		err = destroy(m)
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "\neph: interrupted, destroying %s\n", m.ID)
		err = destroy(m)
	}
	if err != nil {
		return fmt.Errorf("machine %s: %w", m.ID, err)
	}
	fmt.Fprintf(os.Stderr, "eph: machine %s exited cleanly after %s\n", m.ID, m.Uptime().Round(time.Millisecond))
	return nil
}

func cmdRun(argv []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cf := addCommon(fs)
	bootTimeout := fs.Duration("boot-timeout", 30*time.Second, "how long to wait for the guest agent")
	verbose := fs.Bool("v", false, "stream the guest's serial console to stderr")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: eph run [flags] <command> [args...]\n\n"+
			"Boots a machine, runs one command inside it, and destroys it.\n"+
			"Exits with the command's own status.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	cmd := fs.Args()
	if len(cmd) == 0 {
		fs.Usage()
		return fmt.Errorf("no command given")
	}

	cfg, err := cf.config()
	if err != nil {
		return err
	}
	cfg.Init = machine.AgentInit

	// The console is the only place boot failures explain themselves, so keep it
	// even when not streaming — it is the error message if the agent never
	// arrives.
	var console bytes.Buffer
	if *verbose {
		cfg.Console = io.MultiWriter(os.Stderr, &console)
	} else {
		cfg.Console = &console
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	m, err := machine.Boot(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = destroy(m) }()

	readyCtx, cancel := context.WithTimeout(ctx, *bootTimeout)
	defer cancel()
	if err := m.WaitAgent(readyCtx); err != nil {
		return fmt.Errorf("%w\n--- guest console ---\n%s", err, console.String())
	}
	booted := since(started)

	code, err := m.Exec(ctx, cmd, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "eph: machine %s ready in %s, command exited %d\n", m.ID, booted, code)
	if code != 0 {
		return guestExit{code: code}
	}
	return nil
}

// daemonAddr registers the flag every managed command shares.
func daemonAddr(fs *flag.FlagSet) *string {
	return fs.String("addr", envOr("EPHEMERA_ADDR", client.DefaultAddr), "ephemerad address")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func cmdCreate(argv []string) error {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	addr := daemonAddr(fs)
	vcpus := fs.Int("cpus", 0, "vCPU count (0 = daemon default)")
	mem := fs.Int("mem", 0, "memory in MiB (0 = daemon default)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: eph create [flags]\n\nBoots a machine and leaves it running. Prints its id.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := client.New(*addr).Create(ctx, api.CreateRequest{VCPUs: *vcpus, MemMiB: *mem})
	if err != nil {
		return err
	}
	fmt.Println(m.ID)
	return nil
}

func cmdList(argv []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	addr := daemonAddr(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	machines, err := client.New(*addr).List(ctx)
	if err != nil {
		return err
	}
	if len(machines) == 0 {
		fmt.Fprintln(os.Stderr, "no machines running")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tPID\tCPUS\tMEM\tUPTIME")
	for _, m := range machines {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d MiB\t%s\n", m.ID, m.PID, m.VCPUs, m.MemMiB,
			time.Duration(m.UptimeSec*float64(time.Second)).Round(time.Second))
	}
	return tw.Flush()
}

func cmdExec(argv []string) error {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	addr := daemonAddr(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: eph exec [flags] <machine-id> <command> [args...]\n\n"+
			"Runs a command in an existing machine, exiting with the command's status.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		fs.Usage()
		return fmt.Errorf("need a machine id and a command")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	id, cmd := fs.Arg(0), fs.Args()[1:]
	code, err := client.New(*addr).Exec(ctx, id, agent.ExecRequest{Cmd: cmd}, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return guestExit{code: code}
	}
	return nil
}

func cmdRm(argv []string) error {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	addr := daemonAddr(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: eph rm [flags] <machine-id>...\n\nDestroys machines.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("need at least one machine id")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := client.New(*addr)
	// Keep going after a failure so one bad id does not strand the rest.
	var firstErr error
	for _, id := range fs.Args() {
		if err := c.Destroy(ctx, id); err != nil {
			fmt.Fprintf(os.Stderr, "eph: %s: %v\n", id, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		fmt.Println(id)
	}
	return firstErr
}

// guestExit carries a command's non-zero status out to the process exit code
// without being reported as an ephemera failure.
type guestExit struct{ code int }

func (g guestExit) Error() string { return fmt.Sprintf("command exited with status %d", g.code) }

func asGuestExit(err error, out *guestExit) bool {
	if ge, ok := err.(guestExit); ok {
		*out = ge
		return true
	}
	return false
}

func destroy(m *machine.Machine) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.Destroy(ctx)
}

func since(t time.Time) time.Duration { return time.Since(t).Round(time.Millisecond) }
