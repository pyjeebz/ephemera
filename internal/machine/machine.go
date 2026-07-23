// Package machine turns a description of a sandbox into a running microVM.
//
// It sits above the firecracker package: that one knows the VMM's API and
// process, this one knows what an ephemera machine is — its identity, its boot
// arguments, and its lifetime.
package machine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/firecracker"
)

// Defaults kept small deliberately: the dev box has 7.6 GB of RAM, so a machine
// that costs 256 MiB lets us run a useful number of them at once.
const (
	DefaultVCPUs  = 1
	DefaultMemMiB = 256
	DefaultInit   = "/sbin/eph-init"

	// AgentInit boots straight into the guest agent, which is what the daemon
	// uses; the other inits are for a human at a console or for tests.
	AgentInit = "/sbin/eph-agent-init"

	// guestCID identifies the guest on its vsock bus. Host is always 2 and 0/1
	// are reserved, so 3 is the first usable value — and since every machine
	// gets its own VMM and its own socket, they can all share it.
	guestCID = 3
)

// Config describes a machine to boot.
type Config struct {
	KernelPath string // uncompressed ELF vmlinux
	RootfsPath string // ext4 image, attached as /dev/vda
	VCPUs      int
	MemMiB     int

	// Init is the guest's PID 1. The image ships an interactive shell at
	// /sbin/eph-init and a self-terminating check at /sbin/eph-selftest.
	Init string

	// RunDir holds per-machine runtime state, currently just the API socket.
	RunDir string

	// Binary overrides the firecracker executable; empty looks it up on PATH.
	Binary string

	// Console receives the guest's serial output. ConsoleIn is only useful when
	// wired to a real TTY, since Firecracker will not forward piped input.
	Console   io.Writer
	ConsoleIn io.Reader
}

func (c *Config) applyDefaults() {
	if c.VCPUs == 0 {
		c.VCPUs = DefaultVCPUs
	}
	if c.MemMiB == 0 {
		c.MemMiB = DefaultMemMiB
	}
	if c.Init == "" {
		c.Init = DefaultInit
	}
	if c.RunDir == "" {
		c.RunDir = "run"
	}
}

func (c Config) validate() error {
	for _, f := range []struct{ name, path string }{
		{"kernel", c.KernelPath},
		{"rootfs", c.RootfsPath},
	} {
		if f.path == "" {
			return fmt.Errorf("machine: %s path is required", f.name)
		}
		if _, err := os.Stat(f.path); err != nil {
			return fmt.Errorf("machine: %s not usable: %w", f.name, err)
		}
	}
	return nil
}

// Machine is one running microVM.
type Machine struct {
	ID        string
	StartedAt time.Time

	cfg       Config
	vmm       *firecracker.VMM
	vsockPath string
}

// VsockPath is the host socket through which the guest agent is reached.
func (m *Machine) VsockPath() string { return m.vsockPath }

// Pid is the VMM process id, recorded so a restarted daemon can find machines
// it no longer supervises.
func (m *Machine) Pid() int { return m.vmm.Pid() }

// Spec reports the machine's resource shape.
func (m *Machine) Spec() (vcpus, memMiB int) { return m.cfg.VCPUs, m.cfg.MemMiB }

// WaitAgent blocks until the guest agent is accepting commands.
//
// Boot returns as soon as the VMM accepts the start action, long before the
// guest kernel and userland are up, so anything that wants to run a command has
// to wait for the agent rather than for Boot.
func (m *Machine) WaitAgent(ctx context.Context) error {
	return agent.WaitReady(ctx, m.vsockPath)
}

// Exec runs a command in the guest and streams its output.
//
// The returned status is the command's own: non-zero is a result, not an error.
func (m *Machine) Exec(ctx context.Context, cmd []string, stdout, stderr io.Writer) (int, error) {
	return agent.Exec(ctx, m.vsockPath, agent.ExecRequest{Cmd: cmd}, stdout, stderr)
}

// Boot starts a machine and returns once the VMM has accepted the boot action.
// The guest kernel is still starting at that point; callers that need the guest
// itself to be up should watch the console or, from Phase 3, the guest agent.
func Boot(ctx context.Context, cfg Config) (*Machine, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}

	vsockPath := filepath.Join(cfg.RunDir, id+".vsock")
	// The VMM creates this socket itself and refuses to start if one is already
	// there, so clear any remnant of a machine that did not shut down cleanly.
	if err := os.Remove(vsockPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("machine: clear stale vsock socket: %w", err)
	}

	vmm, err := firecracker.Launch(ctx, firecracker.Options{
		Binary:    cfg.Binary,
		SockPath:  filepath.Join(cfg.RunDir, id+".sock"),
		Console:   cfg.Console,
		ConsoleIn: cfg.ConsoleIn,
		Cleanup:   []string{vsockPath},
	})
	if err != nil {
		return nil, err
	}

	// Any failure past this point leaves a live VMM behind, so unwind it.
	fail := func(err error) (*Machine, error) {
		_ = vmm.Shutdown(context.Background())
		return nil, err
	}

	c := vmm.Client()
	if err := c.SetBootSource(ctx, firecracker.BootSource{
		KernelImagePath: cfg.KernelPath,
		BootArgs:        BootArgs(cfg.Init),
	}); err != nil {
		return fail(err)
	}
	if err := c.SetDrive(ctx, firecracker.Drive{
		DriveID:      "rootfs",
		PathOnHost:   cfg.RootfsPath,
		IsRootDevice: true,
		IsReadOnly:   false,
	}); err != nil {
		return fail(err)
	}
	if err := c.SetMachineConfig(ctx, firecracker.MachineConfig{
		VcpuCount:  cfg.VCPUs,
		MemSizeMib: cfg.MemMiB,
	}); err != nil {
		return fail(err)
	}
	// Always attached: it costs nothing when unused, and it is the only way in
	// for a machine with no network interface.
	if err := c.SetVsock(ctx, firecracker.Vsock{
		GuestCID: guestCID,
		UDSPath:  vsockPath,
	}); err != nil {
		return fail(err)
	}
	if err := c.Start(ctx); err != nil {
		return fail(err)
	}

	return &Machine{
		ID:        id,
		StartedAt: time.Now(),
		cfg:       cfg,
		vmm:       vmm,
		vsockPath: vsockPath,
	}, nil
}

// BootArgs builds the guest kernel command line.
//
//	console=ttyS0     kernel log and the guest's stdio land on the serial port
//	reboot=k          route guest resets through the i8042 controller, which is
//	                  what makes Firecracker exit when the guest reboots
//	panic=1           reboot on panic rather than hanging forever
//	pci=off           Firecracker exposes no PCI bus; skip probing for one
func BootArgs(init string) string {
	args := []string{"console=ttyS0", "reboot=k", "panic=1", "pci=off"}
	if init != "" {
		args = append(args, "init="+init)
	}
	return strings.Join(args, " ")
}

// Wait blocks until the machine exits. A guest that resets itself returns nil.
func (m *Machine) Wait() error { return m.vmm.Wait() }

// Done closes when the machine exits for any reason.
func (m *Machine) Done() <-chan struct{} { return m.vmm.Done() }

// Uptime reports how long the machine has been running.
func (m *Machine) Uptime() time.Duration { return time.Since(m.StartedAt) }

// Destroy tears the machine down and releases its runtime state.
func (m *Machine) Destroy(ctx context.Context) error { return m.vmm.Shutdown(ctx) }

// newID returns a short, collision-resistant machine identifier.
func newID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("machine: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
