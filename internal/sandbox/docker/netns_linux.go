package docker

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// dialInNetns connects to addr from inside the network namespace of process
// pid. The socket is created while this OS thread is switched into that
// namespace and keeps belonging to it afterwards. Needs CAP_SYS_ADMIN.
func dialInNetns(ctx context.Context, pid int, addr string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		conn, err, restored := dialLocked(ctx, pid, addr)
		if restored {
			runtime.UnlockOSThread()
		}
		// If the thread could not be switched back, it stays locked and
		// is thrown away when this goroutine exits.
		ch <- result{conn, err}
	}()
	r := <-ch
	return r.conn, r.err
}

func dialLocked(ctx context.Context, pid int, addr string) (conn net.Conn, err error, restored bool) {
	orig, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		return nil, err, true
	}
	defer orig.Close()
	target, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return nil, fmt.Errorf("sandbox network namespace: %w", err), true
	}
	defer target.Close()

	if err := setns(target); err != nil {
		return nil, fmt.Errorf("enter sandbox network namespace: %w", err), true
	}
	conn, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err := setns(orig); err != nil {
		if conn != nil {
			conn.Close()
		}
		return nil, fmt.Errorf("leave sandbox network namespace: %w", err), false
	}
	return conn, dialErr, true
}

func setns(f *os.File) error {
	return unix.Setns(int(f.Fd()), unix.CLONE_NEWNET)
}
