// Command ephemera drives ephemera machines.
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
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/cgroup"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/jail"
	"github.com/pyjeebz/ephemera/internal/machine"
	"github.com/pyjeebz/ephemera/internal/vmnet"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	var err error
	switch args[0] {
	case "new":
		err = cmdNew(args[1:])
	case "list", "ls":
		err = cmdList(args[1:])
	case "ssh", "shell":
		err = cmdSSH(args[1:])
	case "scp":
		err = cmdSCP(args[1:])
	case "exec":
		err = cmdExec(args[1:])
	case "run":
		err = cmdRun(args[1:])
	case "snapshot":
		err = cmdSnapshot(args[1:])
	case "snapshots":
		err = cmdSnapshots(args[1:])
	case "fork":
		err = cmdFork(args[1:])
	case "stop":
		err = cmdStop(args[1:])
	case "start":
		err = cmdStart(args[1:])
	case "rm":
		err = cmdRm(args[1:])
	case "boot":
		err = cmdBoot(args[1:])
	case "desktop":
		err = cmdDesktop(args[1:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "ephemera: unknown command %q\n\n", args[0])
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
		fmt.Fprintln(os.Stderr, "ephemera:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ephemera — spin up boxes: fast, isolated Linux machines you use like a laptop.
(aliased as "eph" — same command, fewer keystrokes.)

usage: ephemera <verb> [args]

  new [name]    spin up a box; give it a name to keep it, omit for a throwaway
  list          your boxes
  ssh <box>     open a terminal in a box
  scp <a> <b>   copy files in or out (box:path <-> local path)
  exec <box> …  run one command in a box
  stop <box>    stop a box, keeping its disk
  start <box>   start a stopped box, state intact
  rm <box>      delete a box
  snapshot <box>  freeze a box to disk
  fork <snap>   clone a box from a snapshot, in milliseconds
  desktop <box> open the box's graphical desktop (coming soon)

  run […] <cmd>   throwaway box: boot, run one command, destroy
  boot            boot a box and stream its console (dev)

run "ephemera <verb> -h" for flags
`)
}

// common holds the flags every command shares.
type common struct {
	kernel *string
	rootfs *string
	vcpus  *int
	mem    *int
	runDir     *string
	net        *bool
	pool       *string
	dns        *string
	cgroupRoot *string
	jail       *bool
}

func addCommon(fs *flag.FlagSet) *common {
	return &common{
		kernel: fs.String("kernel", "build/kernel/vmlinux", "guest kernel (uncompressed ELF vmlinux)"),
		rootfs: fs.String("rootfs", "build/rootfs/rootfs.ext4", "guest root filesystem image"),
		vcpus:  fs.Int("cpus", machine.DefaultVCPUs, "vCPU count"),
		mem:    fs.Int("mem", machine.DefaultMemMiB, "memory in MiB"),
		runDir: fs.String("run-dir", "run", "directory for per-machine runtime state"),
		net:        fs.Bool("net", false, "give the machine a network interface (needs CAP_NET_ADMIN)"),
		pool:       fs.String("pool", vmnet.DefaultPool, "address range the machine's link is carved from"),
		dns:        fs.String("dns", machine.DefaultDNS.String(), "resolver handed to a networked guest"),
		cgroupRoot: fs.String("cgroup-root", cgroup.DefaultRoot, "delegated cgroup subtree for resource caps (empty to disable)"),
		jail:       fs.Bool("jail", false, "confine the VMM to a chroot and its own pid namespace (unprivileged)"),
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

	resolver, err := netip.ParseAddr(*c.dns)
	if err != nil {
		return machine.Config{}, fmt.Errorf("bad -dns: %w", err)
	}

	cfg := machine.Config{
		KernelPath: kernelPath,
		RootfsPath: rootfsPath,
		VCPUs:      *c.vcpus,
		MemMiB:     *c.mem,
		RunDir:     *c.runDir,
		DNS:        resolver,
	}

	// Without -net the machine has no interface at all, the default and the
	// strongest thing on offer. With it, the interface is built by eph-netadmin —
	// eph itself holds no capability, so it checks the helper is usable before
	// promising a network.
	if *c.net {
		netHelper, err := vmnet.HelperPath("")
		if err != nil {
			return machine.Config{}, err
		}
		if err := vmnet.Available(netHelper); err != nil {
			return machine.Config{}, fmt.Errorf("%w\nrun build/host-setup.sh once to grant it", err)
		}
		mgr, err := vmnet.New(*c.pool, netHelper)
		if err != nil {
			return machine.Config{}, err
		}
		cfg.Net = mgr
	}

	// Caps apply whenever the subtree is there — a restriction, not a request —
	// so a plain `eph run` is capped too once host-setup has been run. If the
	// subtree is missing the machine simply runs uncapped; only an explicit -net
	// is ever hard-refused.
	if *c.cgroupRoot != "" {
		caps := cgroup.New(*c.cgroupRoot)
		if caps.Available() == nil {
			cfg.Cgroup = caps
		}
	}

	if *c.jail {
		if err := jail.Available(); err != nil {
			return machine.Config{}, err
		}
		helper, err := jail.HelperPath("")
		if err != nil {
			return machine.Config{}, err
		}
		cfg.Jail, cfg.JailHelper = true, helper
	}
	return cfg, nil
}

func cmdBoot(argv []string) error {
	fs := flag.NewFlagSet("boot", flag.ExitOnError)
	cf := addCommon(fs)
	init := fs.String("init", machine.DefaultInit, "guest init; /sbin/eph-selftest runs a check and halts")
	timeout := fs.Duration("timeout", 0, "destroy the machine after this long (0 = no limit)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera boot [flags]\n\nBoots a machine and streams its console until it exits.\n\nflags:\n")
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
	fmt.Fprintf(os.Stderr, "ephemera: machine %s booted in %s\n", m.ID, since(started))

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
		fmt.Fprintf(os.Stderr, "ephemera: timeout reached, destroying %s\n", m.ID)
		err = destroy(m)
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "\neph: interrupted, destroying %s\n", m.ID)
		err = destroy(m)
	}
	if err != nil {
		return fmt.Errorf("machine %s: %w", m.ID, err)
	}
	fmt.Fprintf(os.Stderr, "ephemera: machine %s exited cleanly after %s\n", m.ID, m.Uptime().Round(time.Millisecond))
	return nil
}

func cmdRun(argv []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cf := addCommon(fs)
	bootTimeout := fs.Duration("boot-timeout", 30*time.Second, "how long to wait for the guest agent")
	verbose := fs.Bool("v", false, "stream the guest's serial console to stderr")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera run [flags] <command> [args...]\n\n"+
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
	fmt.Fprintf(os.Stderr, "ephemera: machine %s ready in %s, command exited %d\n", m.ID, booted, code)
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

func cmdList(argv []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	addr := daemonAddr(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl := client.New(*addr)

	computers, err := cl.ListComputers(ctx)
	if err != nil {
		return err
	}
	machines, err := cl.List(ctx)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "BOX\tKIND\tSTATE\tADDRESS\tAGE")
	rows := 0

	// Named computers — kept boxes — running or stopped.
	for _, c := range computers {
		state, addr := "stopped", "-"
		if c.Running {
			state = "running"
			if c.GuestIP != "" {
				addr = c.GuestIP
			}
		}
		fmt.Fprintf(tw, "%s\tkept\t%s\t%s\t%s\n", c.Name, state, addr, time.Since(c.CreatedAt).Round(time.Second))
		rows++
	}
	// Throwaway machines — those not backing a computer.
	for _, m := range machines {
		if m.Computer != "" {
			continue
		}
		addr := m.GuestIP
		if addr == "" {
			addr = "-"
		}
		fmt.Fprintf(tw, "%s\ttemp\trunning\t%s\t%s\n", m.ID, addr,
			time.Duration(m.UptimeSec*float64(time.Second)).Round(time.Second))
		rows++
	}

	if rows == 0 {
		fmt.Fprintln(os.Stderr, "no boxes — 'ephemera new [name]' to spin one up")
		return nil
	}
	return tw.Flush()
}

func cmdExec(argv []string) error {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	addr := daemonAddr(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera exec [flags] <machine-id> <command> [args...]\n\n"+
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

	// flag stops parsing at the machine id (the first non-flag argument), so a
	// "--" after it is never consumed as the usual end-of-flags marker and
	// arrives as a literal command token. Drop it, so "eph exec id -- cmd" runs
	// cmd rather than trying to exec "--".
	id, cmd := fs.Arg(0), fs.Args()[1:]
	if len(cmd) > 0 && cmd[0] == "--" {
		cmd = cmd[1:]
	}
	if len(cmd) == 0 {
		fs.Usage()
		return fmt.Errorf("need a command to run")
	}
	code, err := client.New(*addr).Exec(ctx, id, agent.ExecRequest{Cmd: cmd}, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return guestExit{code: code}
	}
	return nil
}

func cmdSSH(argv []string) error {
	fs := flag.NewFlagSet("shell", flag.ExitOnError)
	addr := daemonAddr(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera shell [flags] <computer-name | machine-id>\n\n"+
			"Opens an interactive terminal inside a computer or machine — a real shell\n"+
			"with line editing and full-screen programs. Ctrl-D or 'exit' to leave.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("need exactly one computer name or machine id")
	}

	if !isTerminal(os.Stdin) {
		return fmt.Errorf("eph shell needs an interactive terminal")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cl := client.New(*addr)
	target := fs.Arg(0)

	// Resolve the target's agent socket. A name is tried as a computer first —
	// that is the everyday case — and then as a raw machine id.
	vsockPath, label, err := resolveShellTarget(ctx, cl, target)
	if err != nil {
		return err
	}
	if vsockPath == "" {
		return fmt.Errorf("%s has no reachable agent socket", label)
	}

	// Raw mode: the local terminal must stop interpreting keystrokes so they pass
	// through untouched to the guest's pty, which does the echoing and editing.
	rows, cols := termSize(os.Stdout)
	old, err := makeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("set terminal to raw mode: %w", err)
	}
	defer restoreTerm(int(os.Stdin.Fd()), old)

	// Forward terminal resizes: on SIGWINCH, read the new size and push it to the
	// session so the guest's pty follows along and full-screen programs reflow.
	resize := make(chan agent.WinSize, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			r, c := termSize(os.Stdout)
			select {
			case resize <- agent.WinSize{Rows: r, Cols: c}:
			default:
			}
		}
	}()

	fmt.Fprintf(os.Stderr, "ephemera: connected to %s (Ctrl-D or 'exit' to leave)\r\n", label)
	err = agent.Shell(ctx, vsockPath, agent.ExecRequest{
		Rows: rows, Cols: cols, Term: os.Getenv("TERM"),
	}, os.Stdin, os.Stdout, resize)
	restoreTerm(int(os.Stdin.Fd()), old)
	fmt.Fprintf(os.Stderr, "\neph: session ended\n")
	return err
}

// resolveShellTarget finds the agent socket for a shell target, which may be a
// computer name or a machine id. It returns the socket, a label for messages,
// and any error. A computer that exists but is not running is a clear error
// rather than a confusing fall-through to a machine-id lookup.
func resolveShellTarget(ctx context.Context, cl *client.Client, target string) (vsockPath, label string, err error) {
	lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if c, cerr := cl.GetComputer(lookup, target); cerr == nil {
		if !c.Running {
			return "", "", fmt.Errorf("computer %q is stopped — 'ephemera computer start %s' first", target, target)
		}
		return c.VsockPath, "computer " + target, nil
	}

	m, merr := cl.Get(lookup, target)
	if merr != nil {
		return "", "", fmt.Errorf("no computer or machine %q", target)
	}
	return m.VsockPath, "machine " + m.ID, nil
}

func cmdRm(argv []string) error {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	addr := daemonAddr(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera rm [flags] <box>...\n\n"+
			"Deletes boxes. A kept box's disk goes with it; a throwaway just stops.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("need at least one box")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl := client.New(*addr)
	// Keep going after a failure so one bad name does not strand the rest. Each
	// target is tried as a computer first, then as a raw machine id.
	var firstErr error
	for _, box := range fs.Args() {
		var err error
		if _, gerr := cl.GetComputer(ctx, box); gerr == nil {
			err = cl.DeleteComputer(ctx, box)
		} else {
			err = cl.Destroy(ctx, box)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "ephemera: %s: %v\n", box, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		fmt.Println(box)
	}
	return firstErr
}

func cmdSnapshot(argv []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	addr := daemonAddr(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera snapshot [flags] <machine-id>\n\n"+
			"Freezes a running machine to disk and prints the snapshot id. The\n"+
			"machine keeps running. Fork the snapshot to start copies of it.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("need exactly one machine id")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	snap, err := client.New(*addr).Snapshot(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Println(snap.ID)
	return nil
}

func cmdSnapshots(argv []string) error {
	fs := flag.NewFlagSet("snapshots", flag.ExitOnError)
	addr := daemonAddr(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	snaps, err := client.New(*addr).ListSnapshots(ctx)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		fmt.Fprintln(os.Stderr, "no snapshots")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tSOURCE\tCPUS\tMEM\tAGE")
	for _, s := range snaps {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d MiB\t%s\n", s.ID, s.SourceID, s.VCPUs, s.MemMiB,
			time.Since(s.CreatedAt).Round(time.Second))
	}
	return tw.Flush()
}

func cmdFork(argv []string) error {
	fs := flag.NewFlagSet("fork", flag.ExitOnError)
	addr := daemonAddr(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera fork [flags] <snapshot-id>\n\n"+
			"Starts a new machine from a snapshot and prints its id. The copy comes\n"+
			"up ready in milliseconds — its agent was already running when the\n"+
			"snapshot was taken.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("need exactly one snapshot id")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := client.New(*addr).Fork(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Println(m.ID)
	return nil
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
