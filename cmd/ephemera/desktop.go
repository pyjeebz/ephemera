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

// cmdDesktop bridges a box's graphical desktop to a local port so a VNC viewer
// can connect. The pixels leave the box over vsock — the same private channel the
// shell uses — and this re-exposes them as a plain TCP port on localhost for
// whatever viewer you like. Increment 1: bring your own viewer.
func cmdDesktop(argv []string) error {
	fs := flag.NewFlagSet("desktop", flag.ExitOnError)
	addr := daemonAddr(fs)
	port := fs.Int("port", 5900, "local port to expose the desktop on (point a VNC viewer here)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera desktop [flags] <box>\n\n"+
			"Bridges a box's graphical desktop to a local port. Point a VNC viewer at\n"+
			"127.0.0.1:<port> while this runs; Ctrl-C to disconnect.\n\nflags:\n")
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

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return fmt.Errorf("listen on local port %d: %w", *port, err)
	}
	defer ln.Close()
	// Unblock Accept when the user interrupts.
	go func() { <-ctx.Done(); ln.Close() }()

	fmt.Fprintf(os.Stderr, "ephemera: %s desktop ready — point a VNC viewer at 127.0.0.1:%d (Ctrl-C to stop)\n", label, *port)

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

	// The timeout bounds only the vsock CONNECT handshake; Dial clears the deadline
	// once attached, so the long-lived RFB stream is not cut off mid-session.
	guest, err := vsock.Dial(vsockPath, agent.DesktopPort, 10*time.Second)
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
