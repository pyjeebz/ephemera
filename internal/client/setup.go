package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/worktree"
)

// Setup is a command that prepares a sandbox, such as "npm ci". Its result
// is saved as a project snapshot; later sandboxes start from that snapshot
// and skip it. SnapshotID changes whenever what setup reads changes.
type Setup struct {
	Argv       []string
	SnapshotID string
	Key        []byte // stays on the laptop; sent only to the host
}

// LoadSnapshot asks the sandbox's host to start it from a project
// snapshot. An error means hosts without snapshots, or a failure; the
// sandbox is then as it was.
func (c *Client) LoadSnapshot(ctx context.Context, sb *api.Sandbox, id string, key []byte) (api.ProjectSnapshotResult, error) {
	var res api.ProjectSnapshotResult
	resp, err := c.hostDo(ctx, sb, http.MethodPost, "/v1/sandboxes/"+sb.ID+"/snapshots/load",
		jsonBody(api.ProjectSnapshotRequest{ID: id, Key: key}), "application/json")
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	return res, decodeJSON(resp.Body, &res)
}

// SaveSnapshot asks the sandbox's host to save it as a project snapshot.
func (c *Client) SaveSnapshot(ctx context.Context, sb *api.Sandbox, id string, key []byte) error {
	resp, err := c.hostDo(ctx, sb, http.MethodPost, "/v1/sandboxes/"+sb.ID+"/snapshots/save",
		jsonBody(api.ProjectSnapshotRequest{ID: id, Key: key}), "application/json")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ErrSetupFailed means the setup command exited non-zero.
var ErrSetupFailed = errors.New("setup failed")

// setupFiles splits the files to sync into the project's own files and
// the ones only --include adds, such as .env.local. The extra files are
// synced after setup and its snapshot, so they are never in a snapshot.
func setupFiles(dir string, all []string) (base, extra []string, err error) {
	own, err := worktree.Files(dir, nil)
	if err != nil {
		return nil, nil, err
	}
	in := make(map[string]bool, len(own))
	for _, f := range own {
		in[f] = true
	}
	for _, f := range all {
		if in[f] {
			base = append(base, f)
		} else {
			extra = append(extra, f)
		}
	}
	return base, extra, nil
}

// prepare loads the project snapshot, or, after the project's files are
// synced, runs setup and saves one. Files from --include are synced last
// either way, so neither setup nor the snapshot ever sees them.
func (c *Client) prepare(ctx context.Context, sb *api.Sandbox, s *Setup, syncBase, syncExtra func() error, stdout, stderr io.Writer, emit func(string, map[string]any)) error {
	loaded := false
	if s.SnapshotID != "" {
		res, err := c.LoadSnapshot(ctx, sb, s.SnapshotID, s.Key)
		var se *StatusError
		switch {
		case errors.As(err, &se) && se.Code == http.StatusNotImplemented:
			emit("snapshot", map[string]any{"id": s.SnapshotID, "loaded": false, "reason": "this host has no project snapshots"})
		case err != nil:
			emit("snapshot", map[string]any{"id": s.SnapshotID, "loaded": false, "reason": err.Error()})
		default:
			loaded = res.Loaded
			emit("snapshot", map[string]any{"id": s.SnapshotID, "loaded": res.Loaded, "reason": res.Reason})
		}
	}
	// Files are synced either way: the snapshot holds them as they were
	// when it was made.
	if err := syncBase(); err != nil {
		return err
	}
	if loaded {
		return syncExtra()
	}
	code, err := c.Exec(ctx, sb, api.ExecRequest{Argv: s.Argv}, stdout, stderr)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("%w: %v exited with code %d", ErrSetupFailed, s.Argv, code)
	}
	if s.SnapshotID == "" {
		return syncExtra()
	}
	if err := c.SaveSnapshot(ctx, sb, s.SnapshotID, s.Key); err != nil {
		var se *StatusError
		if !(errors.As(err, &se) && se.Code == http.StatusNotImplemented) {
			emit("snapshot_error", map[string]any{"error": err.Error()})
		}
		return syncExtra() // the sandbox is set up; only the snapshot is missing
	}
	emit("snapshot_saved", map[string]any{"id": s.SnapshotID})
	return syncExtra()
}
