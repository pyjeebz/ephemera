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
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/egress"
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

	// Jailed runs every VM under Firecracker's jailer, each as a user ID
	// of its own in a chroot with cgroup limits. Needs root; production
	// hosts always run jailed.
	Jailed bool
	// ChrootBase holds the jails. It must be on the same filesystem as
	// ImageDir, so the image can be hard-linked in. Default
	// /var/lib/ephemera/jail.
	ChrootBase string
	// UIDBase is the first user ID handed to VMs; each VM gets its own
	// from UIDBase to UIDBase+65535. Default 300000.
	UIDBase int

	// Snapshots makes sandboxes of the default size start by restoring a
	// snapshot of a booted VM instead of booting. Prepare builds it.
	Snapshots bool
	// TemplateDir holds the snapshot. Jailed, it must be on the same
	// filesystem as the jails. Default /var/lib/ephemera/templates.
	TemplateDir string
	// Log, if set, receives one line per sandbox start: how and how long.
	Log func(id, how string, d time.Duration)

	tmpl *Snapshot // the template snapshot, once built

	mu      sync.Mutex
	vms     map[string]*VM
	proxies map[string]*vmProxy
	uids    map[int]bool
	next    int
}

// vmProxy is one VM's egress proxy and the socket it listens on.
type vmProxy struct {
	proxy *egress.Proxy
	ln    net.Listener
}

// proxyEnv points programs in the VM at its local proxy. Commands' own
// variables come after, so they can override it.
var proxyEnv = []string{
	"HTTP_PROXY=http://127.0.0.1:3128", "HTTPS_PROXY=http://127.0.0.1:3128",
	"http_proxy=http://127.0.0.1:3128", "https_proxy=http://127.0.0.1:3128",
	"NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1",
}

const cgroupParent = "ephemera"

func (b *Backend) chrootBase() string {
	if b.ChrootBase != "" {
		return b.ChrootBase
	}
	return "/var/lib/ephemera/jail"
}

// allocUID returns a user ID no running VM has.
func (b *Backend) allocUID() (int, error) {
	base := b.UIDBase
	if base == 0 {
		base = 300000
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.uids == nil {
		b.uids = map[int]bool{}
	}
	for i := 0; i < 65536; i++ {
		uid := base + (b.next+i)%65536
		if !b.uids[uid] {
			b.uids[uid] = true
			b.next = (b.next + i + 1) % 65536
			return uid, nil
		}
	}
	return 0, errors.New("no free user IDs for VMs")
}

func (b *Backend) freeUID(uid int) {
	b.mu.Lock()
	delete(b.uids, uid)
	b.mu.Unlock()
}

// jailID turns a sandbox id into one the jailer accepts.
func jailID(id string) string { return strings.ReplaceAll(id, "_", "-") }

var _ sandbox.Backend = (*Backend)(nil)

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
	var policy egress.Policy
	switch spec.Network {
	case api.NetworkAllow:
		for _, pat := range spec.Allow {
			if err := egress.ValidatePattern(pat); err != nil {
				return err
			}
		}
		policy.Allow = spec.Allow
	case api.NetworkOpen:
		policy.Allow = []string{"*"}
	}
	if strings.ContainsAny(id, "/\\.") || id == "" {
		return fmt.Errorf("invalid sandbox id %q", id)
	}
	dir := b.dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	scratch := filepath.Join(dir, "scratch.ext4")
	var err error
	cpus := b.DefaultCPUs
	if spec.CPUs > 0 {
		cpus = int(math.Ceil(spec.CPUs))
	}
	mem := b.DefaultMemoryMB
	if spec.MemoryMB > 0 {
		mem = spec.MemoryMB
	}
	cfg := VMConfig{
		Dir:      dir,
		Kernel:   filepath.Join(b.ImageDir, "vmlinux"),
		RootFS:   filepath.Join(b.ImageDir, "rootfs.ext4"),
		CPUs:     cpus,
		MemoryMB: mem,
	}
	if b.Jailed {
		uid, err := b.allocUID()
		if err != nil {
			os.RemoveAll(dir)
			return err
		}
		cfg.Jail = &Jail{ChrootBase: b.chrootBase(), ID: jailID(id), UID: uid, GID: uid, CgroupParent: cgroupParent}
	}
	start := time.Now()
	var vm *VM
	how := "boot"
	if t := b.tmpl; t != nil && t.CPUs == cpus && t.MemoryMB == mem {
		how = "restore"
		vm, err = Restore(ctx, cfg, t)
		if err != nil {
			// A broken snapshot must not stop sandboxes: boot instead.
			how = "boot after failed restore: " + err.Error()
			vm = nil
		}
	}
	if vm == nil {
		if err = makeScratch(ctx, scratch, b.scratchMB()); err == nil {
			cfg.Scratch = scratch
			vm, err = Boot(ctx, cfg)
		}
	}
	if err != nil {
		if cfg.Jail != nil {
			b.freeUID(cfg.Jail.UID)
		}
		os.RemoveAll(dir)
		return err
	}
	if b.Log != nil {
		b.Log(id, how, time.Since(start))
	}
	var vp *vmProxy
	if spec.Network == api.NetworkAllow || spec.Network == api.NetworkOpen {
		if vp, err = startProxy(vm, policy); err != nil {
			vm.Kill()
			vm.Cleanup()
			if cfg.Jail != nil {
				b.freeUID(cfg.Jail.UID)
			}
			os.RemoveAll(dir)
			return fmt.Errorf("egress proxy: %w", err)
		}
	}
	b.mu.Lock()
	if b.vms == nil {
		b.vms = map[string]*VM{}
		b.proxies = map[string]*vmProxy{}
	}
	b.vms[id] = vm
	if vp != nil {
		b.proxies[id] = vp
	}
	b.mu.Unlock()
	return nil
}

// startProxy listens where Firecracker connects when the guest dials the
// host on the egress port: the vsock socket's path plus "_<port>". In a
// jail, only the VM's own user may connect.
func startProxy(vm *VM, policy egress.Policy) (*vmProxy, error) {
	path := fmt.Sprintf("%s_%d", vm.vsock, guest.EgressPort)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if j := vm.cfg.Jail; j != nil {
		if err := os.Chown(path, j.UID, j.GID); err != nil {
			ln.Close()
			return nil, err
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	p := &egress.Proxy{Policy: policy}
	go p.Serve(ln)
	return &vmProxy{proxy: p, ln: ln}, nil
}

// Connections returns the sandbox's outbound connection attempts, from
// index since on.
func (b *Backend) Connections(id string, since int) ([]api.Connection, error) {
	b.mu.Lock()
	_, ok := b.vms[id]
	vp := b.proxies[id]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("sandbox %s does not exist", id)
	}
	if vp == nil {
		return []api.Connection{}, nil
	}
	return vp.proxy.Connections(since), nil
}

// Prepare builds the template snapshot when Snapshots is on: it boots a VM
// of the default size, waits for its guest agent, and saves it. Sandboxes
// of that size then start from it. It is rebuilt at every start, so it
// always matches the current guest image.
func (b *Backend) Prepare(ctx context.Context) error {
	if !b.Snapshots {
		return nil
	}
	dir := b.TemplateDir
	if dir == "" {
		dir = "/var/lib/ephemera/templates"
	}
	dst := filepath.Join(dir, "default")
	os.RemoveAll(dst)
	vmDir := filepath.Join(b.StateDir, "template")
	os.RemoveAll(vmDir)
	if err := os.MkdirAll(vmDir, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(vmDir)
	scratch := filepath.Join(vmDir, "scratch.ext4")
	if err := makeScratch(ctx, scratch, b.scratchMB()); err != nil {
		return err
	}
	cfg := VMConfig{
		Dir:      vmDir,
		Kernel:   filepath.Join(b.ImageDir, "vmlinux"),
		RootFS:   filepath.Join(b.ImageDir, "rootfs.ext4"),
		Scratch:  scratch,
		CPUs:     max(b.DefaultCPUs, 1),
		MemoryMB: max(b.DefaultMemoryMB, 128),
	}
	if b.Jailed {
		uid, err := b.allocUID()
		if err != nil {
			return err
		}
		defer b.freeUID(uid)
		cfg.Jail = &Jail{ChrootBase: b.chrootBase(), ID: "template", UID: uid, GID: uid, CgroupParent: cgroupParent}
	}
	vm, err := Boot(ctx, cfg)
	if err != nil {
		return fmt.Errorf("boot template: %w", err)
	}
	defer func() { vm.Kill(); vm.Cleanup() }()
	snap, err := vm.CreateSnapshot(ctx, dst)
	if err != nil {
		os.RemoveAll(dst)
		return fmt.Errorf("snapshot template: %w", err)
	}
	b.tmpl = snap
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
	b.mu.Lock()
	_, networked := b.proxies[id]
	b.mu.Unlock()
	if networked {
		req.Env = append(append([]string{}, proxyEnv...), req.Env...)
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
	conn, err := DialGuest(ctx, vm.vsock, guest.Port)
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
	vp := b.proxies[id]
	delete(b.vms, id)
	delete(b.proxies, id)
	b.mu.Unlock()
	if vp != nil {
		vp.ln.Close()
	}
	if ok {
		if err := vm.Kill(); err != nil {
			return err
		}
		vm.Cleanup()
		if vm.cfg.Jail != nil {
			b.freeUID(vm.cfg.Jail.UID)
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
	if b.Jailed {
		// Jails and cgroups of VMs that are gone.
		jails, _ := filepath.Glob(filepath.Join(b.chrootBase(), "*", "*"))
		for _, j := range jails {
			os.RemoveAll(j)
		}
		groups, _ := filepath.Glob(filepath.Join("/sys/fs/cgroup", cgroupParent, "*"))
		for _, g := range groups {
			if fi, err := os.Stat(g); err == nil && fi.IsDir() {
				os.Remove(g)
			}
		}
	}
	return n, errors.Join(errs...)
}

// isFirecracker guards against killing an unrelated process that reused
// a stale pid.
func isFirecracker(pid int) bool {
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	return err == nil && strings.TrimSpace(string(comm)) == "firecracker"
}
