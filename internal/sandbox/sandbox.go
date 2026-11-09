// Package sandbox defines how a host agent runs sandboxes. The host agent
// talks only to Backend, so the container backend used to build ephemera and
// the Firecracker backend it ships with are interchangeable.
package sandbox

import (
	"context"
	"io"
	"net"

	"github.com/pyjeebz/ephemera/internal/api"
)

// Workdir is where the worktree is unpacked and commands run.
const Workdir = "/workspace"

// Backend creates, uses and destroys sandboxes. Implementations must be safe
// for concurrent use across different sandbox ids.
type Backend interface {
	// Create starts an empty sandbox. The spec has been validated and has
	// an image.
	Create(ctx context.Context, id string, spec api.Spec) error
	// CopyIn unpacks a sanitized tar stream into Workdir.
	CopyIn(ctx context.Context, id string, tarStream io.Reader) error
	// Exec runs a command in Workdir and returns its exit code. An error
	// means the command could not be run or was cut short.
	Exec(ctx context.Context, id string, req api.ExecRequest, stdout, stderr io.Writer) (int, error)
	// Dial opens a TCP connection to a port inside the sandbox, as if from
	// the sandbox's own localhost, so servers bound to 127.0.0.1 are
	// reachable and the sandbox needs no network of its own.
	Dial(ctx context.Context, id string, port int) (net.Conn, error)
	// Destroy stops the sandbox and deletes everything it held. Destroying
	// a sandbox that does not exist is not an error.
	Destroy(ctx context.Context, id string) error
}

// NetworkLogger is implemented by backends that record sandboxes'
// outbound connections.
type NetworkLogger interface {
	// Connections returns the sandbox's connection attempts from index
	// since on.
	Connections(id string, since int) ([]api.Connection, error)
}
