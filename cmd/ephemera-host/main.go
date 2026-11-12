// Command ephemera-host is the host agent: it runs sandboxes on this machine
// and keeps a connection open to the central server, which sends it work.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	ver "github.com/pyjeebz/ephemera/internal/version"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/record"
	"github.com/pyjeebz/ephemera/internal/sandbox"
	"github.com/pyjeebz/ephemera/internal/sandbox/docker"
	"github.com/pyjeebz/ephemera/internal/sandbox/firecracker"
)

// version is set at release time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	hostname, _ := os.Hostname()
	var (
		listen    = flag.String("listen", "127.0.0.1:7701", "address to listen on")
		advertise = flag.String("url", "http://127.0.0.1:7701", "URL the CLI uses to reach this host")
		server    = flag.String("server", "http://127.0.0.1:7700", "central server URL")
		name      = flag.String("name", hostname, "host name")
		backend   = flag.String("backend", "docker", "sandbox backend: docker or firecracker")
		image     = flag.String("image", "ubuntu:24.04", "default sandbox image (docker backend)")
		imageDir  = flag.String("image-dir", "/var/lib/ephemera/image", "guest image from deploy/guest/build-image.sh (firecracker backend)")
		stateDir  = flag.String("state-dir", "/var/lib/ephemera/vms", "per-VM working directories (firecracker backend)")
		jail      = flag.Bool("jail", os.Geteuid() == 0, "run each microVM under Firecracker's jailer: its own user, chroot and cgroup limits (needs root)")
		jailDir   = flag.String("jail-dir", "/var/lib/ephemera/jail", "where jails go; same filesystem as -image-dir (firecracker backend)")
		snapshots = flag.Bool("snapshots", true, "start sandboxes from a snapshot of a booted VM (firecracker backend)")
		tmplDir   = flag.String("template-dir", "/var/lib/ephemera/templates", "where the snapshot goes; same filesystem as -jail-dir (firecracker backend)")
		snapDir   = flag.String("snapshot-dir", "/var/lib/ephemera/snapshots", "encrypted project snapshots (firecracker backend)")
		snapMax   = flag.Int64("max-snapshot-bytes", 20<<30, "total size of project snapshots; the least recently used go first")
		capacity  = flag.Int("capacity", 20, "maximum sandboxes on this host")
		keyFile   = flag.String("key-file", "/var/lib/ephemera/host.key", "Ed25519 key that signs sandbox records; created if missing")
		maxLife   = flag.Duration("max-lifetime", 24*time.Hour, "destroy any sandbox this long after it was created (0: no limit)")
	)
	if len(os.Args) > 1 && os.Args[1] == "bench" {
		os.Exit(benchMain(os.Args[2:]))
	}
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("ephemera-host", ver.String(version))
		return
	}
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	secret := os.Getenv("EPHEMERA_HOST_SECRET")
	if secret == "" {
		log.Error("EPHEMERA_HOST_SECRET must be set")
		os.Exit(2)
	}
	be, err := newBackend(*backend, *imageDir, *stateDir, *jail, *jailDir, *snapshots, *tmplDir, log)
	if fc, ok := be.(*firecracker.Backend); ok {
		fc.ProjectSnapshotDir, fc.MaxSnapshotBytes = *snapDir, *snapMax
	}
	if err != nil {
		log.Error("backend", "err", err)
		os.Exit(2)
	}

	key, err := record.LoadOrCreateKey(*keyFile)
	if err != nil {
		log.Error("signing key", "err", err)
		os.Exit(2)
	}
	srv := &host.Server{Backend: be, DefaultImage: *image, Log: log,
		Name: *name, BackendName: *backend, Key: key, MaxLifetime: *maxLife}
	log.Info("records are signed with this host's key", "public_key", record.PublicKey(key))
	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Connect(ctx, host.ConnectConfig{ServerURL: *server, Secret: secret,
		Hello: api.HostHello{Name: *name, URL: *advertise, Capacity: *capacity, Version: ver.String(version)}})
	cleaned := make(chan struct{})
	go func() {
		defer close(cleaned)
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
		// Nothing outlives the host agent.
		if err := srv.DestroyAll(sctx); err != nil {
			log.Error("destroy on shutdown", "err", err)
		}
	}()

	log.Info("host agent listening", "addr", *listen, "backend", *backend, "version", ver.String(version))
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
	<-cleaned
	log.Info("host agent stopped; all sandboxes destroyed")
}

func newBackend(name, imageDir, stateDir string, jail bool, jailDir string, snapshots bool, tmplDir string, log *slog.Logger) (sandbox.Backend, error) {
	switch name {
	case "docker":
		return &docker.Backend{}, nil
	case "firecracker":
		for _, f := range []string{"vmlinux", "rootfs.ext4"} {
			if _, err := os.Stat(filepath.Join(imageDir, f)); err != nil {
				return nil, fmt.Errorf("guest image: %w (build it with deploy/guest/build-image.sh %s)", err, imageDir)
			}
		}
		if jail && os.Geteuid() != 0 {
			return nil, fmt.Errorf("the jailer needs root; run as root or pass -jail=false for development")
		}
		if !jail {
			log.Warn("microVMs run without the jailer: every VM runs as this user, with no chroot or cgroup limits")
		}
		be := &firecracker.Backend{ImageDir: imageDir, StateDir: stateDir, DefaultCPUs: 2, DefaultMemoryMB: 1024,
			Jailed: jail, ChrootBase: jailDir, Snapshots: snapshots, TemplateDir: tmplDir,
			Log: func(id, how string, d time.Duration) {
				log.Info("sandbox started", "sandbox", id, "how", how, "took", d.Round(time.Millisecond))
			}}
		// Nothing from an earlier run may keep running unseen.
		if n, err := be.Cleanup(); err != nil {
			return nil, fmt.Errorf("clean up old VMs: %w", err)
		} else if n > 0 {
			log.Warn("removed microVMs left from an earlier run", "count", n)
		}
		if snapshots {
			start := time.Now()
			if err := be.Prepare(context.Background()); err != nil {
				// Sandboxes still work; they just boot.
				log.Error("snapshot template failed; sandboxes will boot instead", "err", err)
			} else {
				log.Info("snapshot template ready", "took", time.Since(start).Round(time.Millisecond))
			}
		}
		return be, nil
	default:
		return nil, fmt.Errorf("unknown backend %q (available: docker, firecracker)", name)
	}
}
