// Package firecracker runs sandboxes as Firecracker microVMs. Each VM boots
// the guest image with the ephemera guest agent as init, and the host talks
// to the agent over vsock.
package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pyjeebz/ephemera/internal/guest"
)

// VMConfig describes one microVM.
type VMConfig struct {
	Dir    string // per-VM directory: config, sockets, log
	Kernel string // vmlinux
	RootFS string // read-only root filesystem image
	// Scratch is a writable disk image for this VM only; the guest builds
	// its writable root and workspace on it. Optional.
	Scratch  string
	CPUs     int
	MemoryMB int
	// Firecracker is the firecracker binary; default "firecracker".
	Firecracker string
	// Jail, when set, runs Firecracker under the jailer. Needs root.
	Jail *Jail
}

// Jail runs one VM's Firecracker process under Firecracker's jailer: in a
// chroot that holds only that VM's files, as a user ID of its own, with
// cgroup limits on CPU, memory and process count, and seccomp filters
// (Firecracker applies those itself).
type Jail struct {
	Jailer     string // jailer binary; default "jailer"
	ChrootBase string // must be on the same filesystem as the image
	ID         string // letters, digits and hyphens
	UID, GID   int
	// CgroupParent is the cgroup v2 group the VM's cgroup goes under.
	CgroupParent string
}

// chrootRoot is the jail's root directory on the host, laid out the way
// the jailer expects: <base>/<exec file name>/<id>/root.
func (j *Jail) chrootRoot(execFile string) string {
	return filepath.Join(j.ChrootBase, filepath.Base(execFile), j.ID, "root")
}

// cgroupDir is the VM's cgroup, which the jailer creates but never removes.
func (j *Jail) cgroupDir() string {
	return filepath.Join("/sys/fs/cgroup", j.CgroupParent, j.ID)
}

// VM is a running microVM.
type VM struct {
	cfg     VMConfig
	cmd     *exec.Cmd
	vsock   string        // host path of the vsock Unix socket
	jailDir string        // the jail's directory, removed on cleanup
	exited  chan struct{} // closed when Firecracker has exited
	exitErr error
	// HTTP talks to the guest agent over vsock.
	HTTP *http.Client
}

// LogPath is where the VM's console output goes.
func (c *VMConfig) LogPath() string { return filepath.Join(c.Dir, "console.log") }

// Boot starts the VM and waits until the guest agent answers.
func Boot(ctx context.Context, cfg VMConfig) (*VM, error) {
	if cfg.CPUs <= 0 {
		cfg.CPUs = 2
	}
	if cfg.MemoryMB <= 0 {
		cfg.MemoryMB = 1024
	}
	if cfg.Firecracker == "" {
		cfg.Firecracker = "firecracker"
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, err
	}
	vm := &VM{cfg: cfg, exited: make(chan struct{})}
	// paths as Firecracker sees them, and where its files go on the host
	kernel, rootfs, scratch := cfg.Kernel, cfg.RootFS, cfg.Scratch
	confDir := cfg.Dir
	vm.vsock = filepath.Join(cfg.Dir, "vsock.sock")
	vsockInVM := vm.vsock
	var cmd *exec.Cmd
	if cfg.Jail != nil {
		execFile, err := exec.LookPath(cfg.Firecracker)
		if err == nil {
			execFile, err = filepath.Abs(execFile)
		}
		if err != nil {
			return nil, fmt.Errorf("firecracker binary: %w", err)
		}
		root, err := prepareJail(cfg, execFile)
		if err != nil {
			return nil, err
		}
		vm.jailDir = filepath.Dir(root)
		kernel, rootfs, vsockInVM = "/vmlinux", "/rootfs.ext4", "/vsock.sock"
		if scratch != "" {
			scratch = "/scratch.ext4"
		}
		confDir = root
		vm.vsock = filepath.Join(root, "vsock.sock")
		cmd = jailerCommand(cfg, execFile)
	}
	conf := map[string]any{
		"boot-source": map[string]any{
			"kernel_image_path": kernel,
			"boot_args":         "console=ttyS0 reboot=k panic=1 pci=off quiet init=/sbin/ephemera-guest",
		},
		"drives":         drives(rootfs, scratch),
		"machine-config": map[string]any{"vcpu_count": cfg.CPUs, "mem_size_mib": cfg.MemoryMB},
		"vsock":          map[string]any{"guest_cid": 3, "uds_path": vsockInVM},
	}
	b, _ := json.MarshalIndent(conf, "", "  ")
	confPath := filepath.Join(confDir, "vm.json")
	if err := os.WriteFile(confPath, b, 0o644); err != nil {
		vm.removeJail()
		return nil, err
	}
	os.Remove(vm.vsock)
	logf, err := os.Create(cfg.LogPath())
	if err != nil {
		vm.removeJail()
		return nil, err
	}
	if cmd == nil {
		cmd = exec.Command(cfg.Firecracker, "--no-api", "--config-file", confPath)
	}
	cmd.Stdin = nil // the console must not read our input
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		vm.removeJail()
		return nil, fmt.Errorf("start firecracker: %w", err)
	}
	// The pid file lets a restarted host agent find VMs it lost track of.
	os.WriteFile(filepath.Join(cfg.Dir, "firecracker.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	vm.cmd = cmd
	go func() {
		vm.exitErr = cmd.Wait()
		logf.Close()
		close(vm.exited)
	}()
	vm.HTTP = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return DialGuest(ctx, vm.vsock, guest.Port)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}}
	if err := vm.waitReady(ctx); err != nil {
		vm.Kill()
		vm.removeJail()
		return nil, fmt.Errorf("%w (console: %s)", err, tail(cfg.LogPath(), 600))
	}
	return vm, nil
}

func drives(rootfs, scratch string) []any {
	d := []any{map[string]any{
		"drive_id": "rootfs", "path_on_host": rootfs, "is_root_device": true, "is_read_only": true,
	}}
	if scratch != "" {
		d = append(d, map[string]any{
			"drive_id": "scratch", "path_on_host": scratch, "is_root_device": false, "is_read_only": false,
		})
	}
	return d
}

// prepareJail fills the chroot with this VM's files only: hard links to the
// shared kernel and read-only root filesystem, and the VM's own scratch
// disk, owned by the VM's user. It returns the chroot root.
func prepareJail(cfg VMConfig, execFile string) (string, error) {
	j := cfg.Jail
	root := j.chrootRoot(execFile)
	os.RemoveAll(filepath.Dir(root)) // a stale jail of the same id
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		os.RemoveAll(filepath.Dir(root))
		return "", err
	}
	for src, dst := range map[string]string{cfg.Kernel: "vmlinux", cfg.RootFS: "rootfs.ext4"} {
		if err := os.Link(src, filepath.Join(root, dst)); err != nil {
			return fail(fmt.Errorf("link %s into the jail (the jail base must be on the same filesystem as the image): %w", src, err))
		}
	}
	if cfg.Scratch != "" {
		dst := filepath.Join(root, "scratch.ext4")
		if err := os.Rename(cfg.Scratch, dst); err != nil {
			return fail(fmt.Errorf("move scratch disk into the jail: %w", err))
		}
		if err := os.Chown(dst, j.UID, j.GID); err != nil {
			return fail(err)
		}
	}
	// Firecracker creates its vsock socket in the jail's root.
	if err := os.Chown(root, j.UID, j.GID); err != nil {
		return fail(err)
	}
	return root, nil
}

func jailerCommand(cfg VMConfig, execFile string) *exec.Cmd {
	j := cfg.Jail
	jailer := j.Jailer
	if jailer == "" {
		jailer = "jailer"
	}
	memBytes := (int64(cfg.MemoryMB) + 256) << 20 // guest memory plus room for the VMM
	args := []string{
		"--id", j.ID,
		"--exec-file", execFile,
		"--uid", strconv.Itoa(j.UID),
		"--gid", strconv.Itoa(j.GID),
		"--chroot-base-dir", j.ChrootBase,
		"--cgroup-version", "2",
		"--parent-cgroup", j.CgroupParent,
		"--cgroup", "memory.max=" + strconv.FormatInt(memBytes, 10),
		"--cgroup", fmt.Sprintf("cpu.max=%d 100000", cfg.CPUs*100000),
		"--cgroup", "pids.max=128",
		"--resource-limit", "no-file=4096",
		"--",
		"--no-api", "--config-file", "/vm.json",
	}
	return exec.Command(jailer, args...)
}

// removeJail deletes the jail directory and the VM's cgroup.
func (vm *VM) removeJail() {
	if vm.cfg.Jail == nil {
		return
	}
	if vm.jailDir != "" {
		os.RemoveAll(vm.jailDir)
	}
	os.Remove(vm.cfg.Jail.cgroupDir())
}

// Cleanup removes what the VM left on the host once it has stopped.
func (vm *VM) Cleanup() { vm.removeJail() }

// PID is the Firecracker process id.
func (vm *VM) PID() int { return vm.cmd.Process.Pid }

func (vm *VM) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-vm.exited:
			return fmt.Errorf("firecracker exited before the guest was ready: %v", vm.exitErr)
		default:
		}
		rctx, cancel := context.WithTimeout(ctx, time.Second)
		req, _ := http.NewRequestWithContext(rctx, http.MethodGet, "http://guest/healthz", nil)
		resp, err := vm.HTTP.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("guest agent did not answer within 30s: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Kill stops the VM immediately and waits for Firecracker to exit. It is
// safe to call more than once.
func (vm *VM) Kill() error {
	select {
	case <-vm.exited:
		return nil
	default:
	}
	vm.cmd.Process.Kill()
	select {
	case <-vm.exited:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("firecracker did not exit")
	}
}

// Exited is closed when the VM has stopped.
func (vm *VM) Exited() <-chan struct{} { return vm.exited }

// DialGuest connects to a vsock port in the guest through Firecracker's
// Unix socket: connect, send "CONNECT <port>", expect "OK <n>".
func DialGuest(ctx context.Context, udsPath string, port uint32) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", udsPath)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(5 * time.Second))
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, err
	}
	// Read the reply byte by byte so nothing after it is consumed.
	var line []byte
	buf := make([]byte, 1)
	for len(line) < 64 {
		if _, err := conn.Read(buf); err != nil {
			conn.Close()
			return nil, fmt.Errorf("vsock handshake: %w", err)
		}
		if buf[0] == '\n' {
			break
		}
		line = append(line, buf[0])
	}
	if !strings.HasPrefix(string(line), "OK ") {
		conn.Close()
		return nil, fmt.Errorf("vsock handshake: %q", line)
	}
	conn.SetDeadline(time.Time{})
	return conn, nil
}

func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}

var timeZero time.Time
