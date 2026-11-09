package firecracker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/guest"
	"github.com/pyjeebz/ephemera/internal/sandbox"
	"github.com/pyjeebz/ephemera/internal/stream"
)

// Backend runs each sandbox as a Firecracker microVM.
type Backend struct {
	ImageDir string // holds vmlinux and rootfs.ext4 from deploy/guest/build-image.sh
	StateDir string // one directory per running VM
	// ScratchMB is the size of each VM's writable disk. It is a sparse
	// file, so only what the sandbox writes takes space. Default 20 GB.
	ScratchMB int
	// Defaults when a spec sets no limits.
	DefaultCPUs     int
	DefaultMemoryMB int

	mu  sync.Mutex
	vms map[string]*VM
}

var _ sandbox.Backend = (*Backend)(nil)

// ErrNetworkUnsupported is returned for specs that ask for outbound
// network, which microVMs do not have yet.
var ErrNetworkUnsupported = errors.New("outbound network for microVM sandboxes is not available yet; run without --net")

func (b *Backend) vm(id string) (*VM, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	vm, ok := b.vms[id]
	if !ok {
		return nil, fmt.Errorf("sandbox %s does not exist", id)
	}
	return vm, nil
}

func (b *Backend) dir(id string) string { return filepath.Join(b.StateDir, id) }

// Create boots a microVM with its own scratch disk. The spec's image is
// ignored: every VM boots the one guest image.
func (b *Backend) Create(ctx context.Context, id string, spec api.Spec) error {
	if spec.Network == api.NetworkOpen {
		return ErrNetworkUnsupported
	}
	if strings.ContainsAny(id, "/\\.") || id == "" {
		return fmt.Errorf("invalid sandbox id %q", id)
	}
	dir := b.dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	scratch := filepath.Join(dir, "scratch.ext4")
	if err := makeScratch(ctx, scratch, b.scratchMB()); err != nil {
		os.RemoveAll(dir)
		return err
	}
	cpus := b.DefaultCPUs
	if spec.CPUs > 0 {
		cpus = int(math.Ceil(spec.CPUs))
	}
	mem := b.DefaultMemoryMB
	if spec.MemoryMB > 0 {
		mem = spec.MemoryMB
	}
	vm, err := Boot(ctx, VMConfig{
		Dir:      dir,
		Kernel:   filepath.Join(b.ImageDir, "vmlinux"),
		RootFS:   filepath.Join(b.ImageDir, "rootfs.ext4"),
		Scratch:  scratch,
		CPUs:     cpus,
		MemoryMB: mem,
	})
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	b.mu.Lock()
	if b.vms == nil {
		b.vms = map[string]*VM{}
	}
	b.vms[id] = vm
	b.mu.Unlock()
	return nil
}

func (b *Backend) scratchMB() int {
	if b.ScratchMB > 0 {
		return b.ScratchMB
	}
	return 20 * 1024
}

// makeScratch creates an empty, sparse ext4 disk image.
func makeScratch(ctx context.Context, path string, sizeMB int) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = f.Truncate(int64(sizeMB) << 20)
	f.Close()
	if err != nil {
		return err
	}
	mkfs, err := exec.LookPath("mkfs.ext4")
	if err != nil {
		mkfs = "/usr/sbin/mkfs.ext4"
	}
	out, err := exec.CommandContext(ctx, mkfs, "-q", "-F", "-m", "0",
		"-E", "lazy_itable_init=1,lazy_journal_init=1", "-L", "ephemera-scratch", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkfs.ext4: %v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func guestError(resp *http.Response) error {
	var e api.Error
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
	if e.Error == "" {
		e.Error = resp.Status
	}
	return errors.New(e.Error)
}

// CopyIn sends the tar stream to the guest agent.
func (b *Backend) CopyIn(ctx context.Context, id string, tarStream io.Reader) error {
	vm, err := b.vm(id)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://guest/files", tarStream)
	if err != nil {
		return err
	}
	resp, err := vm.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("guest upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("guest upload: %w", guestError(resp))
	}
	return nil
}

// Exec runs a command through the guest agent and relays its output.
func (b *Backend) Exec(ctx context.Context, id string, req api.ExecRequest, stdout, stderr io.Writer) (int, error) {
	vm, err := b.vm(id)
	if err != nil {
		return 0, err
	}
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://guest/exec", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := vm.HTTP.Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("guest exec: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("guest exec: %w", guestError(resp))
	}
	for {
		f, err := stream.Read(resp.Body)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if err == io.EOF {
				return 0, errors.New("guest exec: the VM stopped before the command finished")
			}
			return 0, fmt.Errorf("guest exec: %w", err)
		}
		switch f.Kind {
		case stream.Stdout:
			stdout.Write(f.Data)
		case stream.Stderr:
			stderr.Write(f.Data)
		case stream.Exit:
			return f.ExitCode(), nil
		case stream.Error:
			return 0, fmt.Errorf("guest exec: %s", f.Data)
		}
	}
}

// Dial connects to a port on the guest's localhost through the agent.
func (b *Backend) Dial(ctx context.Context, id string, port int) (net.Conn, error) {
	vm, err := b.vm(id)
	if err != nil {
		return nil, err
	}
	conn, err := DialGuest(ctx, vm.cfg.vsockPath(), guest.Port)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	fmt.Fprintf(conn, "GET /ports/%d HTTP/1.1\r\nHost: guest\r\nConnection: Upgrade\r\nUpgrade: %s\r\n\r\n", port, api.PortUpgrade)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("guest port %d: %w", port, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		err := guestError(resp)
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(timeZero)
	return &bufferedConn{Conn: conn, r: br}, nil
}

// bufferedConn reads first whatever the HTTP reader already buffered.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// CloseWrite passes a half-close on to the guest, so a client that stops
// sending still gets the rest of the reply.
func (c *bufferedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

// Destroy stops the VM and deletes its directory, scratch disk included.
func (b *Backend) Destroy(ctx context.Context, id string) error {
	b.mu.Lock()
	vm, ok := b.vms[id]
	delete(b.vms, id)
	b.mu.Unlock()
	if ok {
		if err := vm.Kill(); err != nil {
			return err
		}
	}
	return os.RemoveAll(b.dir(id))
}

// Cleanup stops VMs left behind by an earlier run of the host agent, for
// example after a crash, and deletes their directories. Call it at start.
func (b *Backend) Cleanup() (int, error) {
	entries, err := os.ReadDir(b.StateDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(b.StateDir, e.Name())
		if raw, err := os.ReadFile(filepath.Join(dir, "firecracker.pid")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && isFirecracker(pid) {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// isFirecracker guards against killing an unrelated process that reused
// a stale pid.
func isFirecracker(pid int) bool {
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	return err == nil && strings.TrimSpace(string(comm)) == "firecracker"
}
