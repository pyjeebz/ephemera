package client

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/worktree"
)

// RunOptions configure Run.
type RunOptions struct {
	Dir     string   // worktree to sync
	Include []string // ignored files to send anyway
	Spec    api.Spec
	Exec    api.ExecRequest
	Stdout  io.Writer
	Stderr  io.Writer
	// Setup, if set, prepares the sandbox first, from a project snapshot
	// when one exists.
	Setup *Setup
	// Events, when set, receives progress: "sandbox", "synced",
	// "snapshot", "snapshot_saved", "snapshot_error",
	// "connection" (one per outbound connection attempt), "destroyed".
	// It is called from the goroutine running Run.
	Events func(event string, fields map[string]any)
}

// RunResult reports what happened.
type RunResult struct {
	SandboxID   string
	Files       int
	ExitCode    int
	Duration    time.Duration
	Connections []api.Connection  // outbound connection attempts
	Record      *api.SignedRecord // signed by the host when it destroyed the sandbox
}

// Run creates a sandbox, syncs the worktree, runs one command, and destroys
// the sandbox, whether or not the command succeeded or ctx was cancelled.
func (c *Client) Run(ctx context.Context, o RunOptions) (res RunResult, err error) {
	start := time.Now()
	emit := func(ev string, f map[string]any) {
		if o.Events != nil {
			o.Events(ev, f)
		}
	}

	files, err := worktree.Files(o.Dir, o.Include)
	if err != nil {
		return res, err
	}
	sb, err := c.Create(ctx, o.Spec)
	if err != nil {
		return res, err
	}
	res.SandboxID = sb.ID
	emit("sandbox", map[string]any{"id": sb.ID, "host": sb.Host})

	defer func() {
		// Always clean up, even when ctx was cancelled by Ctrl-C.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		rec, derr := c.Destroy(dctx, sb.ID)
		res.Record = rec
		if derr != nil {
			if err == nil {
				err = derr
			} else {
				err = fmt.Errorf("%w (and %v)", err, derr)
			}
			return
		}
		emit("destroyed", map[string]any{"id": sb.ID})
	}()

	upload := func(fs []string) error {
		if len(fs) == 0 {
			return nil
		}
		return c.Upload(ctx, sb, o.Dir, fs)
	}
	synced := func() error {
		res.Files = len(files)
		emit("synced", map[string]any{"files": len(files)})
		return nil
	}
	if o.Setup != nil {
		var base, extra []string
		if base, extra, err = setupFiles(o.Dir, files); err == nil {
			err = c.prepare(ctx, sb, o.Setup,
				func() error { return c.Upload(ctx, sb, o.Dir, base) },
				func() error {
					if err := upload(extra); err != nil {
						return err
					}
					return synced()
				}, o.Stdout, o.Stderr, emit)
		}
	} else if err = c.Upload(ctx, sb, o.Dir, files); err == nil {
		err = synced()
	}
	if err != nil {
		return res, err
	}

	res.ExitCode, err = c.Exec(ctx, sb, o.Exec, o.Stdout, o.Stderr)
	res.Duration = time.Since(start)
	if hasNetwork(o.Spec) {
		// Read the log before the sandbox, and its log, are destroyed.
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if conns, lerr := c.Connections(lctx, sb, 0); lerr == nil {
			res.Connections = conns
			for _, cn := range conns {
				emit("connection", connectionFields(cn))
			}
		}
		cancel()
	}
	return res, err
}

func connectionFields(c api.Connection) map[string]any {
	f := map[string]any{"host": c.Host, "port": c.Port, "allowed": c.Allowed, "kind": c.Kind}
	if c.Rule != "" {
		f["rule"] = c.Rule
	}
	if c.Error != "" {
		f["error"] = c.Error
	}
	if c.BytesIn > 0 || c.BytesOut > 0 {
		f["bytes_in"], f["bytes_out"] = c.BytesIn, c.BytesOut
	}
	return f
}

func hasNetwork(s api.Spec) bool {
	return s.Network == api.NetworkAllow || s.Network == api.NetworkOpen || len(s.Allow) > 0
}
