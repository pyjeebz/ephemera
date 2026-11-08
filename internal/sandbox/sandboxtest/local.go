// Package sandboxtest provides a sandbox backend for tests that runs
// commands as local processes in a temporary directory. It isolates nothing
// and must never be used outside tests.
package sandboxtest

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/sandbox"
)

// Local runs each sandbox in its own directory under Root.
type Local struct {
	Root string

	mu   sync.Mutex
	dirs map[string]string
}

var _ sandbox.Backend = (*Local)(nil)

func (l *Local) dir(id string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	d, ok := l.dirs[id]
	if !ok {
		return "", fmt.Errorf("sandbox %s does not exist", id)
	}
	return d, nil
}

// Live returns how many sandboxes exist.
func (l *Local) Live() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.dirs)
}

func (l *Local) Create(_ context.Context, id string, _ api.Spec) error {
	d := filepath.Join(l.Root, id)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dirs == nil {
		l.dirs = map[string]string{}
	}
	l.dirs[id] = d
	return nil
}

func (l *Local) CopyIn(_ context.Context, id string, r io.Reader) error {
	d, err := l.dir(id)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p := filepath.Join(d, filepath.FromSlash(hdr.Name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(p, 0o755)
		case tar.TypeSymlink:
			err = os.Symlink(hdr.Linkname, p)
		case tar.TypeReg:
			var f *os.File
			f, err = os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err == nil {
				_, err = io.Copy(f, tr)
				f.Close()
			}
		}
		if err != nil {
			return err
		}
	}
}

func (l *Local) Exec(ctx context.Context, id string, req api.ExecRequest, stdout, stderr io.Writer) (int, error) {
	d, err := l.dir(id)
	if err != nil {
		return 0, err
	}
	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	cmd.Dir = d
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		return exitErr.ExitCode(), nil
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return 0, err
}

// Dial connects to the port on this machine's localhost.
func (l *Local) Dial(ctx context.Context, id string, port int) (net.Conn, error) {
	if _, err := l.dir(id); err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

func (l *Local) Destroy(_ context.Context, id string) error {
	l.mu.Lock()
	d, ok := l.dirs[id]
	delete(l.dirs, id)
	l.mu.Unlock()
	if !ok {
		return nil
	}
	return os.RemoveAll(d)
}
