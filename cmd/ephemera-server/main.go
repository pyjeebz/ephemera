// Command ephemera-server is the central server: it tracks hosts and places
// sandboxes on them. It never receives files, command output or secrets.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/control"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:7700", "address to listen on")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	srv := &control.Server{
		UserToken:  os.Getenv("EPHEMERA_TOKEN"),
		HostSecret: os.Getenv("EPHEMERA_HOST_SECRET"),
		Client:     &http.Client{Timeout: 2 * time.Minute},
		Log:        log,
	}
	if srv.UserToken == "" || srv.HostSecret == "" {
		log.Error("EPHEMERA_TOKEN and EPHEMERA_HOST_SECRET must be set")
		os.Exit(2)
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
	log.Info("central server listening", "addr", *listen)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
