// Command ephemera-host is the host agent: it runs sandboxes on this machine
// and registers with the central server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/sandbox"
	"github.com/pyjeebz/ephemera/internal/sandbox/docker"
)

func main() {
	hostname, _ := os.Hostname()
	var (
		listen    = flag.String("listen", "127.0.0.1:7701", "address to listen on")
		advertise = flag.String("url", "http://127.0.0.1:7701", "URL the CLI and central server use to reach this host")
		server    = flag.String("server", "http://127.0.0.1:7700", "central server URL")
		name      = flag.String("name", hostname, "host name")
		backend   = flag.String("backend", "docker", "sandbox backend: docker")
		image     = flag.String("image", "ubuntu:24.04", "default sandbox image")
		capacity  = flag.Int("capacity", 20, "maximum sandboxes on this host")
	)
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	secret := os.Getenv("EPHEMERA_HOST_SECRET")
	if secret == "" {
		log.Error("EPHEMERA_HOST_SECRET must be set")
		os.Exit(2)
	}
	be, err := newBackend(*backend)
	if err != nil {
		log.Error("backend", "err", err)
		os.Exit(2)
	}

	srv := &host.Server{Backend: be, Secret: secret, DefaultImage: *image, Log: log}
	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go host.Register(ctx, &http.Client{Timeout: 10 * time.Second}, *server, secret,
		api.RegisterHostRequest{Name: *name, URL: *advertise, Capacity: *capacity}, 15*time.Second, log)
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

	log.Info("host agent listening", "addr", *listen, "backend", *backend)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
	<-cleaned
	log.Info("host agent stopped; all sandboxes destroyed")
}

func newBackend(name string) (sandbox.Backend, error) {
	switch name {
	case "docker":
		return &docker.Backend{}, nil
	default:
		return nil, fmt.Errorf("unknown backend %q (available: docker)", name)
	}
}
