package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/worktree"
)

// PortMap forwards a port on the laptop to a port inside the sandbox.
type PortMap struct {
	Local  int // 0 picks a free port
	Remote int
}

// ParsePort parses "3000" or "8080:3000" (local:sandbox).
func ParsePort(s string) (PortMap, error) {
	local, remote, found := cutLast(s, ":")
	if !found {
		local, remote = s, s
	}
	l, err1 := strconv.Atoi(local)
	r, err2 := strconv.Atoi(remote)
	if err1 != nil || err2 != nil || l < 0 || l > 65535 || r < 1 || r > 65535 {
		return PortMap{}, fmt.Errorf("invalid port %q: use PORT or LOCAL:SANDBOX", s)
	}
	return PortMap{Local: l, Remote: r}, nil
}

func cutLast(s, sep string) (string, string, bool) {
	for i := len(s) - len(sep); i >= 0; i-- {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

// DevOptions configure Dev.
type DevOptions struct {
	Dir     string
	Include []string
	Spec    api.Spec
	Exec    api.ExecRequest // the dev server command
	Ports   []PortMap
	// ListenHost is where forwarded ports listen; default 127.0.0.1.
	ListenHost string
	// SyncInterval is how often the worktree is checked for changes;
	// default 500ms.
	SyncInterval time.Duration
	Stdout       io.Writer
	Stderr       io.Writer
	// Events receives progress: "sandbox", "synced", "forwarding",
	// "sync_error", "port_error", "destroyed". It may be called from
	// several goroutines, never at the same time.
	Events func(event string, fields map[string]any)
}

// Dev runs a long-lived command, usually a dev server, in a sandbox. It
// forwards ports to the laptop and keeps sending changed files until the
// command exits or ctx is cancelled, then destroys the sandbox.
func (c *Client) Dev(ctx context.Context, o DevOptions) (code int, err error) {
	var emitMu sync.Mutex
	emit := func(ev string, f map[string]any) {
		if o.Events == nil {
			return
		}
		emitMu.Lock()
		defer emitMu.Unlock()
		o.Events(ev, f)
	}
	if o.ListenHost == "" {
		o.ListenHost = "127.0.0.1"
	}
	if o.SyncInterval <= 0 {
		o.SyncInterval = 500 * time.Millisecond
	}

	snap, err := worktree.Scan(o.Dir, o.Include)
	if err != nil {
		return 0, err
	}
	// Claim the local ports first, so a port already in use fails before
	// a sandbox exists.
	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}()
	for _, p := range o.Ports {
		ln, err := net.Listen("tcp", net.JoinHostPort(o.ListenHost, strconv.Itoa(p.Local)))
		if err != nil {
			return 0, fmt.Errorf("forward port %d: %w", p.Remote, err)
		}
		listeners = append(listeners, ln)
	}

	sb, err := c.Create(ctx, o.Spec)
	if err != nil {
		return 0, err
	}
	emit("sandbox", map[string]any{"id": sb.ID, "host": sb.Host})
	defer func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if derr := c.Destroy(dctx, sb.ID); derr != nil {
			err = errors.Join(err, derr)
			return
		}
		emit("destroyed", map[string]any{"id": sb.ID})
	}()

	if err := c.Upload(ctx, sb, o.Dir, snap.Files()); err != nil {
		return 0, err
	}
	emit("synced", map[string]any{"files": len(snap), "deleted": 0, "initial": true})

	bg, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { stop(); wg.Wait() }()

	for i, ln := range listeners {
		remote := o.Ports[i].Remote
		emit("forwarding", map[string]any{"local": ln.Addr().String(), "port": remote})
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.forward(bg, ln, sb, remote, emit)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.watch(bg, o, sb, snap, emit)
	}()

	return c.Exec(ctx, sb, o.Exec, o.Stdout, o.Stderr)
}

// watch sends changed and deleted files until ctx is done.
func (c *Client) watch(ctx context.Context, o DevOptions, sb *api.Sandbox, snap worktree.Snapshot, emit func(string, map[string]any)) {
	t := time.NewTicker(o.SyncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		next, err := worktree.Scan(o.Dir, o.Include)
		if err != nil {
			emit("sync_error", map[string]any{"error": err.Error()})
			continue
		}
		changed, removed := worktree.Diff(snap, next)
		if len(changed) == 0 && len(removed) == 0 {
			continue
		}
		if len(changed) > 0 {
			err = c.Upload(ctx, sb, o.Dir, changed)
		}
		if err == nil && len(removed) > 0 {
			err = c.DeleteFiles(ctx, sb, removed)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			emit("sync_error", map[string]any{"error": err.Error()})
			continue // the next scan retries the same changes
		}
		snap = next
		emit("synced", map[string]any{"files": len(changed), "deleted": len(removed)})
	}
}

// forward accepts local connections and tunnels each to the sandbox port.
func (c *Client) forward(ctx context.Context, ln net.Listener, sb *api.Sandbox, port int, emit func(string, map[string]any)) {
	go func() { <-ctx.Done(); ln.Close() }()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			remote, err := c.DialPort(ctx, sb, port)
			if err != nil {
				conn.Close()
				if ctx.Err() == nil {
					emit("port_error", map[string]any{"port": port, "error": err.Error()})
				}
				return
			}
			stopOnDone := context.AfterFunc(ctx, func() { conn.Close(); remote.Close() })
			defer stopOnDone()
			pipe(conn, remote)
		}()
	}
}

// pipe copies both ways. When the sandbox side ends, both are closed. When
// the laptop side stops sending, the sandbox gets a grace period to finish
// its reply.
func pipe(local net.Conn, remote io.ReadWriteCloser) {
	done := make(chan struct{})
	go func() {
		io.Copy(local, remote)
		local.Close() // also unblocks the copy below
		remote.Close()
		close(done)
	}()
	io.Copy(remote, local)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		local.Close()
		remote.Close()
		<-done
	}
}
