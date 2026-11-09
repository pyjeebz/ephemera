package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/mcp"
)

// cmdMCP serves ephemera's tools to a coding agent over MCP on stdin and
// stdout. Everything else goes to stderr.
func cmdMCP(ctx context.Context, c *client.Client, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".", "worktree that run_command syncs by default")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	srv := &mcp.Server{
		Client:     c,
		Dir:        *dir,
		Version:    version,
		SaveRecord: func(sr *api.SignedRecord) (string, error) { return saveRecord(sr, "") },
		VerifyRecord: func(ctx context.Context, path string) (string, bool, error) {
			return verifyFile(ctx, c, path)
		},
	}
	if err := srv.Serve(ctx, stdin, stdout); err != nil {
		fmt.Fprintln(stderr, "ephemera mcp:", err)
		return exitFailure
	}
	return 0
}
