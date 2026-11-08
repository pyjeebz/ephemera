// Package docker is a sandbox backend that runs each sandbox as a Docker
// container. It needs no KVM, which makes it the backend for building and
// testing ephemera. Containers share the host kernel, so it is never the
// secure option: production sandboxes run on the Firecracker backend.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/sandbox"
)

// Backend runs sandboxes with the docker CLI.
type Backend struct {
	// Bin is the docker binary. Empty means "docker" on PATH.
	Bin string
}

var _ sandbox.Backend = (*Backend)(nil)

func (b *Backend) bin() string {
	if b.Bin != "" {
		return b.Bin
	}
	return "docker"
}

func container(id string) string { return "ephemera-" + id }

func (b *Backend) run(ctx context.Context, stdin io.Reader, args ...string) error {
	cmd := exec.CommandContext(ctx, b.bin(), args...)
	cmd.Stdin = stdin
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %v: %s", args[0], err, strings.TrimSpace(out.String()))
	}
	return nil
}

// Create starts a container that idles until commands are run in it.
func (b *Backend) Create(ctx context.Context, id string, spec api.Spec) error {
	args := []string{"run", "--detach", "--init",
		"--name", container(id),
		"--label", "ephemera.sandbox=" + id,
		"--workdir", sandbox.Workdir,
		"--security-opt", "no-new-privileges",
		"--cap-drop", "NET_RAW",
	}
	if spec.Network != api.NetworkOpen {
		args = append(args, "--network", "none")
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(spec.CPUs, 'f', -1, 64))
	}
	if spec.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(spec.MemoryMB)+"m")
	}
	args = append(args, spec.Image, "tail", "-f", "/dev/null")
	return b.run(ctx, nil, args...)
}

// CopyIn unpacks the tar stream into the container's workspace.
func (b *Backend) CopyIn(ctx context.Context, id string, tarStream io.Reader) error {
	return b.run(ctx, tarStream, "cp", "-", container(id)+":"+sandbox.Workdir)
}

// Exec runs a command in the container's workspace.
func (b *Backend) Exec(ctx context.Context, id string, req api.ExecRequest, stdout, stderr io.Writer) (int, error) {
	if len(req.Argv) == 0 {
		return 0, errors.New("docker exec: empty command")
	}
	args := []string{"exec", "--workdir", sandbox.Workdir}
	for _, kv := range req.Env {
		args = append(args, "--env", kv)
	}
	args = append(args, container(id))
	args = append(args, req.Argv...)
	cmd := exec.CommandContext(ctx, b.bin(), args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 0, fmt.Errorf("docker exec: %w", err)
	}
	return 0, nil
}

// Dial connects to a port on the container's own localhost by entering its
// network namespace, which works even with networking turned off.
func (b *Backend) Dial(ctx context.Context, id string, port int) (net.Conn, error) {
	out, err := exec.CommandContext(ctx, b.bin(), "inspect", "--format", "{{.State.Pid}}", container(id)).Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return nil, fmt.Errorf("sandbox %s is not running", id)
	}
	return dialInNetns(ctx, pid, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// Destroy removes the container and its filesystem.
func (b *Backend) Destroy(ctx context.Context, id string) error {
	err := b.run(ctx, nil, "rm", "--force", "--volumes", container(id))
	if err != nil && strings.Contains(err.Error(), "No such container") {
		return nil
	}
	return err
}
