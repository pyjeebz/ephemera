// Package firecracker runs sandboxes as Firecracker microVMs. Each VM boots
// the guest image with the ephemera guest agent as init, or is restored from
// a snapshot of one, and the host talks to the agent over vsock.
package firecracker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Every VM's files live in one directory, its root: the jail's chroot, or
// for an unjailed VM its own directory, where Firecracker runs. Inside it
// the files always have these names, and Firecracker is given them as
// relative paths. A snapshot records the paths it was taken with, so this
// sameness is what lets one snapshot be restored into any VM.
const (
	fileKernel  = "vmlinux"
	fileRootFS  = "rootfs.ext4"
	fileScratch = "scratch.ext4"
	fileVsock   = "vsock.sock"
	fileAPI     = "api.sock"
	fileConfig  = "vm.json"
	fileSnapMem = "snap.mem"
	fileSnapVM  = "snap.vmstate"
	// A new snapshot is written under these names first: a VM restored
	// from the template has the template's shared, read-only snap.* files
	// in its root, which must never be written to.
	fileSaveMem  = "save.mem"
	fileSaveVM   = "save.vmstate"
	bootArgs     = "console=ttyS0 reboot=k panic=1 pci=off quiet init=/sbin/ephemera-guest"
	guestCID     = 3
	readyTimeout = 30 * time.Second
)

// VMConfig describes one microVM.
type VMConfig struct {
	Dir    string // per-VM directory on the host: console log, pid file
	Kernel string // vmlinux
	RootFS string // read-only root filesystem image
	// Scratch is a writable disk image for this VM only; the guest builds
	// its writable root and workspace on it. Boot moves it into the VM's
	// root. Optional.
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
	root    string // the VM's root directory on the host
	vsock   string // host path of the vsock Unix socket
	jailDir string // the jail's directory, removed on cleanup
	exited  chan struct{}
	exitErr error
	// HTTP talks to the guest agent over vsock.
	HTTP *http.Client
	api  *http.Client // Firecracker's own API
}

// LogPath is where the VM's console output goes.
func (c *VMConfig) LogPath() string { return filepath.Join(c.Dir, "console.log") }

func (c *VMConfig) defaults() {
	if c.CPUs <= 0 {
		c.CPUs = 2
	}
	if c.MemoryMB <= 0 {
		c.MemoryMB = 1024
	}
	if c.Firecracker == "" {
		c.Firecracker = "firecracker"
	}
}

// Boot starts a VM from the kernel and waits until the guest agent answers.
func Boot(ctx context.Context, cfg VMConfig) (*VM, error) {
	cfg.defaults()
	vm, err := prepare(cfg, map[string]string{fileKernel: cfg.Kernel, fileRootFS: cfg.RootFS})
	if err != nil {
		return nil, err
	}
	if cfg.Scratch != "" {
		if err := vm.adoptScratch(func(dst string) error { return os.Rename(cfg.Scratch, dst) }); err != nil {
			vm.removeJail()
			return nil, err
		}
	}
	conf := map[string]any{
		"boot-source":    map[string]any{"kernel_image_path": fileKernel, "boot_args": bootArgs},
		"drives":         drives(cfg.Scratch != ""),
		"machine-config": map[string]any{"vcpu_count": cfg.CPUs, "mem_size_mib": cfg.MemoryMB},
		"vsock":          map[string]any{"guest_cid": guestCID, "uds_path": fileVsock},
	}
	b, _ := json.MarshalIndent(conf, "", "  ")
	if err := os.WriteFile(filepath.Join(vm.root, fileConfig), b, 0o644); err != nil {
		vm.removeJail()
		return nil, err
	}
	if err := vm.start("--config-file", fileConfig); err != nil {
		vm.removeJail()
		return nil, err
	}
	if err := vm.waitReady(ctx); err != nil {
		return nil, vm.fail(err)
	}
	return vm, nil
}

// Snapshot is a paused VM's saved state: its memory, its device state, and
// its scratch disk as it was at that moment, plus the size it ran with.
type Snapshot struct {
	Dir      string
	CPUs     int
	MemoryMB int
	// Own means the files belong to the VM restored from them: they are
	// moved into its root rather than shared, and go when it does. A
	// decrypted project snapshot is used this way.
	Own bool
}

// CreateSnapshot pauses the VM and saves it into dir. The VM stays paused;
// kill it afterwards.
func (vm *VM) CreateSnapshot(ctx context.Context, dir string) (*Snapshot, error) {
	vm.HTTP.CloseIdleConnections() // no vsock connection may outlive the snapshot
	if err := vm.apiCall(ctx, http.MethodPatch, "/vm", map[string]any{"state": "Paused"}); err != nil {
		return nil, fmt.Errorf("pause: %w", err)
	}
	for _, f := range []string{fileSaveMem, fileSaveVM} {
		os.Remove(filepath.Join(vm.root, f))
	}
	if err := vm.apiCall(ctx, http.MethodPut, "/snapshot/create", map[string]any{
		"snapshot_type": "Full", "snapshot_path": fileSaveVM, "mem_file_path": fileSaveMem,
	}); err != nil {
		return nil, fmt.Errorf("create snapshot: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for src, dst := range map[string]string{fileSaveMem: fileSnapMem, fileSaveVM: fileSnapVM} {
		if err := moveFile(filepath.Join(vm.root, src), filepath.Join(dir, dst)); err != nil {
			return nil, err
		}
	}
	if vm.hasScratch() {
		// The scratch disk must match the memory: copy it while paused.
		if err := sparseCopy(filepath.Join(vm.root, fileScratch), filepath.Join(dir, fileScratch)); err != nil {
			return nil, err
		}
	}
	for _, f := range []string{fileSnapMem, fileSnapVM, fileScratch} {
		p := filepath.Join(dir, f)
		if _, err := os.Stat(p); err == nil {
			os.Chown(p, 0, 0)
			os.Chmod(p, 0o644) // shared, read-only, by every clone
		}
	}
	return &Snapshot{Dir: dir, CPUs: vm.cfg.CPUs, MemoryMB: vm.cfg.MemoryMB}, nil
}

// Restore starts a VM from a snapshot, with its own copy of the snapshot's
// scratch disk, then sets its clock and reseeds its randomness.
func Restore(ctx context.Context, cfg VMConfig, snap *Snapshot) (*VM, error) {
	cfg.CPUs, cfg.MemoryMB = snap.CPUs, snap.MemoryMB
	cfg.defaults()
	shared := map[string]string{fileRootFS: cfg.RootFS}
	if !snap.Own {
		shared[fileSnapMem] = filepath.Join(snap.Dir, fileSnapMem)
		shared[fileSnapVM] = filepath.Join(snap.Dir, fileSnapVM)
	}
	vm, err := prepare(cfg, shared)
	if err != nil {
		return nil, err
	}
	if snap.Own {
		for _, f := range []string{fileSnapMem, fileSnapVM} {
			if err := moveFile(filepath.Join(snap.Dir, f), filepath.Join(vm.root, f)); err != nil {
				vm.removeJail()
				return nil, err
			}
			if j := cfg.Jail; j != nil {
				os.Chown(filepath.Join(vm.root, f), j.UID, j.GID)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(snap.Dir, fileScratch)); err == nil {
		place := func(dst string) error { return sparseCopy(filepath.Join(snap.Dir, fileScratch), dst) }
		if snap.Own {
			place = func(dst string) error { return moveFile(filepath.Join(snap.Dir, fileScratch), dst) }
		}
		if err := vm.adoptScratch(place); err != nil {
			vm.removeJail()
			return nil, err
		}
	}
	if err := vm.start(); err != nil {
		vm.removeJail()
		return nil, err
	}
	if err := vm.waitAPI(ctx); err != nil {
		return nil, vm.fail(err)
	}
	if err := vm.apiCall(ctx, http.MethodPut, "/snapshot/load", map[string]any{
		"snapshot_path": fileSnapVM,
		"mem_backend":   map[string]any{"backend_type": "File", "backend_path": fileSnapMem},
		"resume_vm":     true,
	}); err != nil {
		return nil, vm.fail(fmt.Errorf("load snapshot: %w", err))
	}
	if err := vm.waitReady(ctx); err != nil {
		return nil, vm.fail(err)
	}
	if err := vm.afterRestore(ctx); err != nil {
		return nil, vm.fail(err)
	}
	return vm, nil
}

// Resume continues a paused VM, such as one just snapshotted.
func (vm *VM) Resume(ctx context.Context) error {
	if err := vm.apiCall(ctx, http.MethodPatch, "/vm", map[string]any{"state": "Resumed"}); err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	return nil
}

func (vm *VM) afterRestore(ctx context.Context) error {
	seed := make([]byte, 64)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	body, _ := json.Marshal(guest.RestoreRequest{UnixNano: time.Now().UnixNano(), Seed: seed})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://guest/restore", bytes.NewReader(body))
	resp, err := vm.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("guest restore: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("guest restore: %w", guestError(resp))
	}
	return nil
}

// prepare makes the VM's root directory and puts the given files in it
// under their fixed names: hard links in a jail (a jail cannot follow a
// link out of itself), symlinks otherwise.
func prepare(cfg VMConfig, files map[string]string) (*VM, error) {
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, err
	}
	vm := &VM{cfg: cfg, exited: make(chan struct{})}
	vm.root = cfg.Dir
	if cfg.Jail != nil {
		execFile, err := exec.LookPath(cfg.Firecracker)
		if err == nil {
			execFile, err = filepath.Abs(execFile)
		}
		if err != nil {
			return nil, fmt.Errorf("firecracker binary: %w", err)
		}
		vm.cfg.Firecracker = execFile
		vm.root = cfg.Jail.chrootRoot(execFile)
		vm.jailDir = filepath.Dir(vm.root)
		os.RemoveAll(vm.jailDir) // a stale jail of the same id
		if err := os.MkdirAll(vm.root, 0o755); err != nil {
			return nil, err
		}
	}
	for name, src := range files {
		dst := filepath.Join(vm.root, name)
		os.Remove(dst)
		var err error
		if cfg.Jail != nil {
			err = os.Link(src, dst)
			if err != nil {
				err = fmt.Errorf("link %s into the jail (the jail base must be on the same filesystem as the image and snapshots): %w", src, err)
			}
		} else {
			abs, _ := filepath.Abs(src)
			err = os.Symlink(abs, dst)
		}
		if err != nil {
			vm.removeJail()
			return nil, err
		}
	}
	if cfg.Jail != nil {
		// Firecracker creates its sockets in the jail's root.
		if err := os.Chown(vm.root, cfg.Jail.UID, cfg.Jail.GID); err != nil {
			vm.removeJail()
			return nil, err
		}
	}
	vm.vsock = filepath.Join(vm.root, fileVsock)
	for _, f := range []string{fileVsock, fileAPI} {
		os.Remove(filepath.Join(vm.root, f))
	}
	vm.HTTP = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return DialGuest(ctx, vm.vsock, guest.Port)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}}
	apiSock := filepath.Join(vm.root, fileAPI)
	vm.api = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", apiSock)
		},
	}}
	return vm, nil
}

// adoptScratch puts the VM's scratch disk in its root with place, owned by
// the VM's user.
func (vm *VM) adoptScratch(place func(dst string) error) error {
	dst := filepath.Join(vm.root, fileScratch)
	if err := place(dst); err != nil {
		return fmt.Errorf("scratch disk: %w", err)
	}
	if j := vm.cfg.Jail; j != nil {
		return os.Chown(dst, j.UID, j.GID)
	}
	return nil
}

func (vm *VM) hasScratch() bool {
	_, err := os.Stat(filepath.Join(vm.root, fileScratch))
	return err == nil
}

// start runs Firecracker, under the jailer if configured, with the API
// socket and any extra arguments.
func (vm *VM) start(extra ...string) error {
	logf, err := os.Create(vm.cfg.LogPath())
	if err != nil {
		return err
	}
	fcArgs := append([]string{"--api-sock", fileAPI}, extra...)
	var cmd *exec.Cmd
	if vm.cfg.Jail != nil {
		cmd = jailerCommand(vm.cfg, vm.cfg.Firecracker, fcArgs)
	} else {
		cmd = exec.Command(vm.cfg.Firecracker, fcArgs...)
		cmd.Dir = vm.root
	}
	cmd.Stdin = nil // the console must not read our input
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return fmt.Errorf("start firecracker: %w", err)
	}
	vm.cmd = cmd
	// The pid file lets a restarted host agent find VMs it lost track of.
	os.WriteFile(filepath.Join(vm.cfg.Dir, "firecracker.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	go func() {
		vm.exitErr = cmd.Wait()
		logf.Close()
		close(vm.exited)
	}()
	return nil
}

// fail stops a VM that did not come up and adds its console to the error.
func (vm *VM) fail(err error) error {
	vm.Kill()
	vm.removeJail()
	return fmt.Errorf("%w (console: %s)", err, tail(vm.cfg.LogPath(), 600))
}

func drives(scratch bool) []any {
	d := []any{map[string]any{
		"drive_id": "rootfs", "path_on_host": fileRootFS, "is_root_device": true, "is_read_only": true,
	}}
	if scratch {
		d = append(d, map[string]any{
			"drive_id": "scratch", "path_on_host": fileScratch, "is_root_device": false, "is_read_only": false,
		})
	}
	return d
}

func jailerCommand(cfg VMConfig, execFile string, fcArgs []string) *exec.Cmd {
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
	}
	return exec.Command(jailer, append(args, fcArgs...)...)
}

// apiCall sends one request to Firecracker's API.
func (vm *VM) apiCall(ctx context.Context, method, path string, body any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, method, "http://firecracker"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := vm.api.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(msg))
	}
	return nil
}

// waitAPI waits until Firecracker's API socket answers.
func (vm *VM) waitAPI(ctx context.Context) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-vm.exited:
			return fmt.Errorf("firecracker exited: %v", vm.exitErr)
		default:
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://firecracker/", nil)
		if resp, err := vm.api.Do(req); err == nil {
			resp.Body.Close()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("firecracker's API did not come up")
		}
		time.Sleep(2 * time.Millisecond)
	}
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
	deadline := time.Now().Add(readyTimeout)
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
			return fmt.Errorf("guest agent did not answer within %v: %v", readyTimeout, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Kill stops the VM immediately and waits for Firecracker to exit. It is
// safe to call more than once.
func (vm *VM) Kill() error {
	if vm.cmd == nil {
		return nil
	}
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

// sparseCopy copies a disk image, keeping its holes, so a mostly empty
// 20 GB image copies in moments.
func sparseCopy(src, dst string) error {
	os.Remove(dst)
	out, err := exec.Command("cp", "--sparse=always", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy %s: %v: %s", src, err, bytes.TrimSpace(out))
	}
	return nil
}

// moveFile renames, or copies and removes across filesystems.
func moveFile(src, dst string) error {
	os.Remove(dst)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := sparseCopy(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
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
