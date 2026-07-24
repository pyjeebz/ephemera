package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/vsock"
)

// cmdDesktop opens a box's graphical desktop. The pixels leave the box over
// vsock — the same private channel the shell uses — and this re-exposes them
// locally: in a browser by default (a self-hosted page over a WebSocket), or as a
// raw VNC port for a native viewer with --raw.
func cmdDesktop(argv []string) error {
	fs := flag.NewFlagSet("desktop", flag.ExitOnError)
	addr := daemonAddr(fs)
	raw := fs.Bool("raw", false, "expose a raw VNC port for a native viewer instead of the browser")
	open := fs.Bool("open", true, "open the desktop in a browser (browser mode only)")
	port := fs.Int("port", 0, "local port to bind (0 picks a free one; raw mode defaults to 5900)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera desktop [flags] <box>\n\n"+
			"Opens a box's graphical desktop in your browser. With --raw it instead\n"+
			"exposes a plain VNC port for a native viewer. Ctrl-C to disconnect.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("need exactly one box")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A desktop box is a throwaway machine or a computer; resolve its agent socket
	// the same way the shell does.
	vsockPath, label, err := resolveShellTarget(ctx, client.New(*addr), fs.Arg(0))
	if err != nil {
		return err
	}
	if vsockPath == "" {
		return fmt.Errorf("%s has no reachable agent socket", label)
	}

	if *raw {
		p := *port
		if p == 0 {
			p = 5900 // the conventional VNC port, what a viewer expects by default
		}
		return runRawBridge(ctx, vsockPath, label, p)
	}
	return runWebDesktop(ctx, vsockPath, label, *port, *open)
}

// runRawBridge exposes the desktop as a plain TCP port and splices each viewer
// connection to the box's desktop vsock port — for native VNC viewers.
func runRawBridge(ctx context.Context, vsockPath, label string, port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen on local port %d: %w", port, err)
	}
	defer ln.Close()
	go func() { <-ctx.Done(); ln.Close() }() // unblock Accept on interrupt

	fmt.Fprintf(os.Stderr, "ephemera: %s desktop ready — point a VNC viewer at 127.0.0.1:%d (Ctrl-C to stop)\n", label, port)

	for {
		local, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // interrupted; a clean stop, not a failure
			}
			return fmt.Errorf("accept: %w", err)
		}
		go bridgeDesktopConn(vsockPath, local)
	}
}

// bridgeDesktopConn splices one viewer connection to the box's desktop vsock port,
// in both directions, until either side closes.
func bridgeDesktopConn(vsockPath string, local net.Conn) {
	defer local.Close()

	guest, err := dialDesktop(vsockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ephemera: desktop connect failed: %v\n", err)
		return
	}
	defer guest.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(guest, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, guest); done <- struct{}{} }()
	<-done
}

// dialDesktop opens the box's desktop vsock port. The timeout bounds only the
// vsock CONNECT handshake; Dial clears the deadline once attached, so the
// long-lived RFB stream is not cut off mid-session.
func dialDesktop(vsockPath string) (net.Conn, error) {
	return vsock.Dial(vsockPath, agent.DesktopPort, 10*time.Second)
}
