package cli

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"

	awaystore "ephemera.run/ephemera/internal/away"
	cachestore "ephemera.run/ephemera/internal/cache"
	"ephemera.run/ephemera/internal/hub"
	"ephemera.run/ephemera/internal/ratelimit"
	"ephemera.run/ephemera/internal/relay"
	"ephemera.run/ephemera/internal/share"
	"ephemera.run/ephemera/internal/wsconn"
)

// serveFlags holds the flags of the relay serve subcommand.
type serveFlags struct {
	addr       string
	tcp        string // optional raw TCP bridge listener (host:port)
	adminToken string
	cacheMB    int64
	awayMB     int64
}

// runServe is the relay binary's main: receiver-facing HTTP surface
// plus the daemon WS bridge at /daemon.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f serveFlags
	fs.StringVar(&f.addr, "addr", ":7100", "HTTP listen address")
	fs.StringVar(&f.tcp, "tcp", "", "also accept raw framed TCP daemon connections on host:port (dev/test)")
	fs.StringVar(&f.adminToken, "admin-token", os.Getenv("EPHEMERA_ADMIN_TOKEN"), "admin API token (or EPHEMERA_ADMIN_TOKEN); empty disables admin routes")
	fs.Int64Var(&f.cacheMB, "cache-mb", 256, "layer-B volatile cache budget in MiB (0 disables replay)")
	fs.Int64Var(&f.awayMB, "away-mb", 512, "layer-C away-prefix budget in MiB (0 disables away mode)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	reg := share.NewRegistry(time.Second)
	bridge := &hub.Bridge{Registry: reg}

	// daemon bridge: one WS connection, one share
	daemonBridge := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		bridge.Accept(wsconn.Wrap(c))
	})

	cachepkg := cachestore.New(f.cacheMB << 20)
	awaypkg := awaystore.New(f.awayMB << 20)
	if f.cacheMB > 0 {
		reg.OnExpire(cachepkg.DropShare)
	}
	if f.awayMB > 0 {
		reg.OnExpire(awaypkg.DropShare)
		bridge.Away = awaypkg
	}
	surface := relay.New(relay.ServerConfig{
		Registry:   reg,
		Limiter:    ratelimit.New(20, 40), // 20 r/s sustained, burst 40 per IP
		AdminToken: f.adminToken,
		Cache:      cachepkg,
		Away:       awaypkg,
	})

	mux := http.NewServeMux()
	mux.Handle("/daemon", daemonBridge)
	mux.Handle("/", surface)

	if f.tcp != "" {
		ln, err := net.Listen("tcp", f.tcp)
		if err != nil {
			return err
		}
		go acceptLoop(ln, bridge)
	}

	fmt.Fprintf(os.Stderr, "relay listening on %s\n", f.addr)
	srv := &http.Server{
		Addr:              f.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}

// acceptLoop bridges raw framed TCP daemon connections (dev/test).
func acceptLoop(ln net.Listener, bridge *hub.Bridge) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			bridge.Accept(conn)
		}()
	}
}

func init() {
	register("serve", runServe)
}
