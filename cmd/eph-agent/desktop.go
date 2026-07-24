package main

import (
	"errors"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// vncAddr is where the in-guest VNC server (x11vnc) listens. It binds localhost
// only, so the sole path to it is this bridge — the desktop is as unreachable
// from any network as the rest of the box.
const vncAddr = "127.0.0.1:5900"

// bridgeDesktop accepts connections on the desktop vsock port and splices each to
// the guest's VNC server. It runs only on a desktop box; on any other box nothing
// is listening on vncAddr, so a connection is accepted and immediately dropped.
//
// Unlike the exec accept loop, an error here must not be fatal: the agent is
// usually PID 1, and losing the desktop bridge is no reason to panic the kernel.
func bridgeDesktop(lfd int) {
	for {
		cfd, _, err := unix.Accept4(lfd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ECONNABORTED) {
				continue
			}
			return // stop bridging; do not take the agent down with it
		}
		go serveDesktop(cfd)
	}
}

// serveDesktop splices one vsock connection to a fresh connection to the VNC
// server, in both directions, until either side closes.
func serveDesktop(fd int) {
	f := os.NewFile(uintptr(fd), "vsock-desktop")
	defer f.Close()

	// Right after boot x11vnc may not have bound its port yet, so give it a short
	// window rather than dropping the first viewer to connect.
	var vnc net.Conn
	var err error
	for range 50 {
		vnc, err = net.DialTimeout("tcp", vncAddr, time.Second)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return // nothing listening — this box has no desktop, or it is not up yet
	}
	defer vnc.Close()

	// Splice both ways. When either direction ends, the deferred closes tear the
	// other down, so the second io.Copy unblocks and its goroutine exits.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(vnc, f); done <- struct{}{} }()
	go func() { _, _ = io.Copy(f, vnc); done <- struct{}{} }()
	<-done
}
