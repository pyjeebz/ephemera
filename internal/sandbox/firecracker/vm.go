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
	"strings"
	"time"

	"github.com/pyjeebz/ephemera/internal/guest"
)

// VMConfig describes one microVM.
type VMConfig struct {
	Dir      string // per-VM directory: config, sockets, log
	Kernel   string // vmlinux
	RootFS   string // read-only root filesystem image
	CPUs     int
	MemoryMB int
	// Firecracker is the firecracker binary; default "firecracker".
	Firecracker string
}

// VM is a running microVM.
type VM struct {
	cfg  VMConfig
	cmd  *exec.Cmd
	done chan error
	// HTTP talks to the guest agent over vsock.
	HTTP *http.Client
}

func (c *VMConfig) vsockPath() string { return filepath.Join(c.Dir, "vsock.sock") }

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
	conf := map[string]any{
		"boot-source": map[string]any{
			"kernel_image_path": cfg.Kernel,
			"boot_args":         "console=ttyS0 reboot=k panic=1 pci=off quiet init=/sbin/ephemera-guest",
		},
		"drives": []any{map[string]any{
			"drive_id": "rootfs", "path_on_host": cfg.RootFS, "is_root_device": true, "is_read_only": true,
		}},
		"machine-config": map[string]any{"vcpu_count": cfg.CPUs, "mem_size_mib": cfg.MemoryMB},
		"vsock":          map[string]any{"guest_cid": 3, "uds_path": cfg.vsockPath()},
	}
	b, _ := json.MarshalIndent(conf, "", "  ")
	confPath := filepath.Join(cfg.Dir, "vm.json")
	if err := os.WriteFile(confPath, b, 0o600); err != nil {
		return nil, err
	}
	os.Remove(cfg.vsockPath())
	logf, err := os.Create(cfg.LogPath())
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(cfg.Firecracker, "--no-api", "--config-file", confPath)
	cmd.Stdin = nil // the console must not read our input
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, fmt.Errorf("start firecracker: %w", err)
	}
	vm := &VM{cfg: cfg, cmd: cmd, done: make(chan error, 1)}
	go func() { vm.done <- cmd.Wait(); logf.Close() }()
	vm.HTTP = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return DialGuest(ctx, cfg.vsockPath(), guest.Port)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}}
	if err := vm.waitReady(ctx); err != nil {
		vm.Kill()
		return nil, fmt.Errorf("%w (console: %s)", err, tail(cfg.LogPath(), 600))
	}
	return vm, nil
}

func (vm *VM) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-vm.done:
			vm.done <- err
			return fmt.Errorf("firecracker exited before the guest was ready: %v", err)
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

// Kill stops the VM immediately and waits for Firecracker to exit.
func (vm *VM) Kill() error {
	if vm.cmd.Process != nil {
		vm.cmd.Process.Kill()
	}
	select {
	case <-vm.done:
	case <-time.After(10 * time.Second):
		return errors.New("firecracker did not exit")
	}
	return nil
}

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
