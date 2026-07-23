// Command ephemerad is ephemera's control plane.
//
// It owns machine lifecycle: booting microVMs, tracking them, running commands
// inside them, and destroying them. Clients talk to it over a Unix socket by
// default — a local-first daemon has no reason to be on the network, and file
// permissions are a better access control than an open port.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/cgroup"
	"github.com/pyjeebz/ephemera/internal/machine"
	"github.com/pyjeebz/ephemera/internal/store"
	"github.com/pyjeebz/ephemera/internal/vmnet"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ephemerad:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "unix://run/ephemerad.sock", "listen address (unix://path or tcp://host:port)")
	kernel := flag.String("kernel", "build/kernel/vmlinux", "guest kernel (uncompressed ELF vmlinux)")
	rootfs := flag.String("rootfs", "build/rootfs/rootfs.ext4", "guest root filesystem image")
	runDir := flag.String("run-dir", "run", "directory for per-machine runtime state")
	logLevel := flag.String("log-level", "info", "debug, info, warn, or error")
	network := flag.Bool("network", false, "allow machines to request a network interface")
	pool := flag.String("pool", vmnet.DefaultPool, "address range machine links are carved from")
	dns := flag.String("dns", machine.DefaultDNS.String(), "resolver handed to networked guests")
	cgroupRoot := flag.String("cgroup-root", cgroup.DefaultRoot, "delegated cgroup v2 subtree for resource caps (empty to disable)")
	flag.Parse()

	log := newLogger(*logLevel)

	kernelPath, rootfsPath, err := resolveImages(*kernel, *rootfs)
	if err != nil {
		return err
	}

	resolver, err := netip.ParseAddr(*dns)
	if err != nil {
		return fmt.Errorf("bad -dns: %w", err)
	}

	// Networking is refused up front rather than on the first create: a daemon
	// that cannot do what it was started to do should say so while someone is
	// still looking at its output.
	var machineNet *vmnet.Manager
	if *network {
		if err := vmnet.Available(); err != nil {
			return fmt.Errorf("%w\nrun build/host-setup.sh once to grant it", err)
		}
		if machineNet, err = vmnet.New(*pool); err != nil {
			return err
		}
		log.Info("machine networking enabled", "pool", *pool, "dns", resolver)
	}

	// Resource caps are a restriction, not a grant, so they apply automatically
	// when the delegated subtree is there rather than on request. A daemon that
	// cannot find it says so once and runs machines uncapped — capping is
	// defence in depth, not a precondition for booting anything.
	var caps *cgroup.Manager
	if *cgroupRoot != "" {
		mgr := cgroup.New(*cgroupRoot)
		if err := mgr.Available(); err != nil {
			log.Warn("resource caps disabled", "reason", err)
		} else {
			caps = mgr
			log.Info("resource caps enabled", "cgroup_root", mgr.Root())
		}
	}

	// Records live beside the sockets they describe, one directory per concern.
	st, err := store.Open(filepath.Join(*runDir, "machines"))
	if err != nil {
		return err
	}

	// Anything still running from a previous daemon is unowned: this process
	// cannot wait on a VMM it did not spawn, so orphans are destroyed, not
	// resumed. Doing it before serving means a fresh daemon starts from a
	// known-empty world.
	reaped, err := store.Reap(filepath.Join(*runDir, "machines"), log)
	if err != nil {
		return err
	}
	if reaped > 0 {
		log.Info("reaped orphaned machines from a previous run", "count", reaped)
	}

	srv := api.New(api.Config{
		KernelPath: kernelPath,
		RootfsPath: rootfsPath,
		RunDir:     *runDir,
		Net:        machineNet,
		DNS:        resolver,
		Cgroup:     caps,
	}, st, log)

	ln, err := listen(*addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	httpSrv := &http.Server{Handler: srv.Handler()}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr, "kernel", kernelPath, "rootfs", rootfsPath)
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Stop accepting first, then take the machines down: a client mid-request
	// gets a clean end rather than a machine vanishing underneath it.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "err", err)
	}
	srv.DestroyAll(shutdownCtx)
	log.Info("stopped")
	return nil
}

// listen opens the control socket. A Unix socket is removed first: the daemon
// owns the path, and a leftover from an unclean exit would otherwise make every
// restart fail with "address already in use".
func listen(addr string) (net.Listener, error) {
	switch {
	case strings.HasPrefix(addr, "unix://"):
		path := strings.TrimPrefix(addr, "unix://")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("prepare socket dir: %w", err)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("clear stale control socket: %w", err)
		}
		ln, err := net.Listen("unix", path)
		if err != nil {
			return nil, fmt.Errorf("listen on %s: %w", addr, err)
		}
		// Owner-only: the socket is the full control plane, so filesystem
		// permissions are what stands between it and anyone else on the box.
		if err := os.Chmod(path, 0o600); err != nil {
			ln.Close()
			return nil, fmt.Errorf("restrict control socket: %w", err)
		}
		return ln, nil

	case strings.HasPrefix(addr, "tcp://"):
		ln, err := net.Listen("tcp", strings.TrimPrefix(addr, "tcp://"))
		if err != nil {
			return nil, fmt.Errorf("listen on %s: %w", addr, err)
		}
		return ln, nil

	default:
		return nil, fmt.Errorf("unsupported address %q: use unix://path or tcp://host:port", addr)
	}
}

// resolveImages makes the boot images absolute and checks they exist, so a bad
// path fails at startup rather than on the first create.
func resolveImages(kernel, rootfs string) (string, string, error) {
	kernelPath, err := filepath.Abs(kernel)
	if err != nil {
		return "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(kernelPath); err == nil {
		kernelPath = resolved
	}
	rootfsPath, err := filepath.Abs(rootfs)
	if err != nil {
		return "", "", err
	}
	for name, p := range map[string]string{"kernel": kernelPath, "rootfs": rootfsPath} {
		if _, err := os.Stat(p); err != nil {
			return "", "", fmt.Errorf("%s not usable: %w", name, err)
		}
	}
	return kernelPath, rootfsPath, nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
