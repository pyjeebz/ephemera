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
	// Events, when set, receives progress: "sandbox", "synced",
	// "destroyed". It is called from the goroutine running Run.
	Events func(event string, fields map[string]any)
}

// RunResult reports what happened.
type RunResult struct {
	SandboxID string
	Files     int
	ExitCode  int
	Duration  time.Duration
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
		if derr := c.Destroy(dctx, sb.ID); derr != nil {
			if err == nil {
				err = derr
			} else {
				err = fmt.Errorf("%w (and %v)", err, derr)
			}
			return
		}
		emit("destroyed", map[string]any{"id": sb.ID})
	}()

	if err = c.Upload(ctx, sb, o.Dir, files); err != nil {
		return res, err
	}
	res.Files = len(files)
	emit("synced", map[string]any{"files": len(files)})

	res.ExitCode, err = c.Exec(ctx, sb, o.Exec, o.Stdout, o.Stderr)
	res.Duration = time.Since(start)
	return res, err
}
