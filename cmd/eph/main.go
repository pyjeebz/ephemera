// Command eph drives ephemera machines.
//
// Phase 1 talks to the machine package directly so the VM path can be exercised
// before ephemerad exists; later it becomes a client of the daemon's API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/machine"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "eph:", err)
		os.Exit(1)
	}
}

func run(argv []string) error {
	fs := flag.NewFlagSet("eph boot", flag.ExitOnError)
	kernel := fs.String("kernel", "build/kernel/vmlinux", "guest kernel (uncompressed ELF vmlinux)")
	rootfs := fs.String("rootfs", "build/rootfs/rootfs.ext4", "guest root filesystem image")
	init := fs.String("init", machine.DefaultInit, "guest init; /sbin/eph-selftest runs a check and halts")
	vcpus := fs.Int("cpus", machine.DefaultVCPUs, "vCPU count")
	mem := fs.Int("mem", machine.DefaultMemMiB, "memory in MiB")
	runDir := fs.String("run-dir", "run", "directory for per-machine runtime state")
	timeout := fs.Duration("timeout", 0, "destroy the machine after this long (0 = no limit)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: eph boot [flags]\n\nBoots a microVM and streams its console until it exits.\n\nflags:\n")
		fs.PrintDefaults()
	}

	// Only one subcommand so far; accept it optionally to keep the shape.
	if len(argv) > 0 && argv[0] == "boot" {
		argv = argv[1:]
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}

	// Firecracker opens these paths itself, so resolve them before handing over
	// to get a clear error here rather than a fault message from the VMM.
	kernelPath, err := filepath.Abs(*kernel)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(kernelPath); err == nil {
		kernelPath = resolved
	}
	rootfsPath, err := filepath.Abs(*rootfs)
	if err != nil {
		return err
	}

	// Ctrl-C should destroy the machine, not orphan it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	m, err := machine.Boot(ctx, machine.Config{
		KernelPath: kernelPath,
		RootfsPath: rootfsPath,
		VCPUs:      *vcpus,
		MemMiB:     *mem,
		Init:       *init,
		RunDir:     *runDir,
		Console:    os.Stdout,
		ConsoleIn:  os.Stdin,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "eph: machine %s booted in %s (pid path %s)\n",
		m.ID, time.Since(started).Round(time.Millisecond), filepath.Join(*runDir, m.ID+".sock"))

	var deadline <-chan time.Time
	if *timeout > 0 {
		t := time.NewTimer(*timeout)
		defer t.Stop()
		deadline = t.C
	}

	// Whichever happens first: the guest exits, the deadline passes, or the
	// operator interrupts. The last two require an explicit teardown.
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
	fmt.Fprintf(os.Stderr, "eph: machine %s exited cleanly after %s\n",
		m.ID, m.Uptime().Round(time.Millisecond))
	return nil
}

func destroy(m *machine.Machine) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.Destroy(ctx)
}
