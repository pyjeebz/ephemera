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
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/cgroup"
	"github.com/pyjeebz/ephemera/internal/firecracker"
	"github.com/pyjeebz/ephemera/internal/jail"
	"github.com/pyjeebz/ephemera/internal/vmnet"
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

	// netIface is the id we give the virtio-net device. Firecracker allows
	// several; a machine gets exactly one, so the name is a constant.
	netIface = "eth0"

	// guestHostname is set by the kernel from the ip= parameter, before init.
	guestHostname = "ephemera"

	// overlayInit is the guest's real PID 1. It makes the root a RAM-backed
	// overlay over the read-only disk, then execs the init named in eph.init=.
	// Every machine boots through it, which is what keeps the disk image
	// read-only and shareable — the fix for both fork's per-copy disk and the
	// corruption two machines would cause sharing one writable image.
	overlayInit = "/sbin/eph-overlay-init"
)

// DefaultDNS is the resolver a networked guest is pointed at. It is handed over
// on the kernel command line rather than baked into the image, so a machine's
// firewall rules and its resolver cannot drift apart.
var DefaultDNS = netip.MustParseAddr("1.1.1.1")

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

	// Net, when set, gives the machine a network interface. Leaving it nil is
	// the strongest isolation on offer and stays the default: a machine with no
	// interface at all cannot be firewalled wrong.
	Net *vmnet.Manager

	// DNS is the resolver the guest is told to use. Zero means DefaultDNS.
	DNS netip.Addr

	// Cgroup, when set, caps the machine's host CPU and memory. Unlike the
	// network it is a restriction rather than a grant, so it applies whenever
	// the subtree is available rather than on explicit request.
	Cgroup *cgroup.Manager

	// Jail confines the VMM to a chroot and its own process tree, entered through
	// a user namespace so it needs no privilege. JailHelper is the eph-jail
	// binary that does the entering; both must be set together.
	Jail       bool
	JailHelper string
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
	if !c.DNS.IsValid() {
		c.DNS = DefaultDNS
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

	lease    vmnet.Lease
	hasLease bool
	cg       *cgroup.Cgroup
	jail     *jail.Spec
	released sync.Once
}

// Jailed reports whether the machine runs confined, and where its jail is.
func (m *Machine) Jailed() (string, bool) {
	if m.jail == nil {
		return "", false
	}
	return string(m.jail.Dir), true
}

// VsockPath is the host socket through which the guest agent is reached.
func (m *Machine) VsockPath() string { return m.vsockPath }

// Lease reports the machine's network link, if it has one.
func (m *Machine) Lease() (vmnet.Lease, bool) { return m.lease, m.hasLease }

// CgroupPath reports the machine's cgroup, empty when it has none.
func (m *Machine) CgroupPath() string {
	if m.cg == nil {
		return ""
	}
	return m.cg.Path()
}

// release frees the machine's host resources: its network link and its cgroup.
//
// Both share the VMM process's lifetime — the TAP is only useful while the VMM
// holds it, and a cgroup cannot be removed until the VMM inside it is gone — and
// a machine can end without anyone calling Destroy, because a guest that resets
// itself takes the VMM with it. So this runs from whichever path gets there
// first, and only once. It must not run before the VMM has exited, which every
// caller arranges.
func (m *Machine) release() {
	m.released.Do(func() {
		if m.hasLease {
			_ = m.cfg.Net.Detach(m.lease)
		}
		if m.cg != nil {
			_ = m.cg.Destroy()
		}
	})
}

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

// Shell opens an interactive terminal session in the guest, relaying between the
// given local input/output and a shell on a pseudo-terminal inside the machine.
// req carries the terminal's initial size and type.
func (m *Machine) Shell(ctx context.Context, req agent.ExecRequest, in io.Reader, out io.Writer) error {
	return agent.Shell(ctx, m.vsockPath, req, in, out)
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

	m := &Machine{ID: id, StartedAt: time.Now(), cfg: cfg}

	// The interface has to exist and be up before the VMM is told to open it,
	// and doing it first means a host that cannot build networks fails before
	// anything has been spawned.
	if cfg.Net != nil {
		lease, err := cfg.Net.Attach(id)
		if err != nil {
			return nil, err
		}
		m.lease, m.hasLease = lease, true
	}

	// The cgroup exists with its limits written before the VMM is spawned into
	// it, so the process is capped from its first instruction. If this fails,
	// unwind the link we may already hold.
	if cfg.Cgroup != nil {
		cg, err := cfg.Cgroup.Create(id, cgroup.Limits{MemMiB: cfg.MemMiB, VCPUs: cfg.VCPUs})
		if err != nil {
			m.release()
			return nil, err
		}
		m.cg = cg
	}

	// Resolve where the VMM's sockets and images live. Without a jail these are
	// host paths the VMM uses directly; with one they are paths inside the jail,
	// and the daemon reaches the sockets at their host location under the jail
	// directory. kernelPath and rootfsPath are what the VMM is told; vsockForVMM
	// likewise, while vsockPath is always the host path the agent connects to.
	vsockPath := filepath.Join(cfg.RunDir, id+".vsock")
	vsockForVMM := vsockPath
	kernelPath, rootfsPath := cfg.KernelPath, cfg.RootfsPath
	var cleanup []string

	if cfg.Jail {
		fcBin := cfg.Binary
		if fcBin == "" {
			resolved, err := exec.LookPath(firecracker.DefaultBinary)
			if err != nil {
				m.release()
				return nil, fmt.Errorf("machine: locate firecracker for jail: %w", err)
			}
			fcBin = resolved
		}
		spec := &jail.Spec{
			Dir:       jail.Dir(filepath.Join(cfg.RunDir, "jails", id)),
			Binary:    fcBin,
			Kernel:    cfg.KernelPath,
			Rootfs:    cfg.RootfsPath,
			Network:   m.hasLease,
			APISock:   "run/firecracker.sock",
			VsockSock: "run/vsock.sock",
		}
		m.jail = spec
		// Inside the jail the VMM sees fixed names; the real files are bind
		// mounted there by eph-jail, and the sockets land under the jail dir.
		kernelPath, rootfsPath = jail.GuestKernel, jail.GuestRootfs
		vsockForVMM = spec.GuestVsock()
		vsockPath = spec.HostVsockSock()
	} else {
		// The VMM creates this socket itself and refuses to start if one is
		// already there, so clear any remnant of a machine that did not shut down
		// cleanly. A jail always builds a fresh directory, so it has none.
		if err := os.Remove(vsockPath); err != nil && !os.IsNotExist(err) {
			m.release()
			return nil, fmt.Errorf("machine: clear stale vsock socket: %w", err)
		}
		cleanup = []string{vsockPath}
	}

	var cgroupFD *int
	if m.cg != nil {
		fd := m.cg.FD()
		cgroupFD = &fd
	}
	vmm, err := firecracker.Launch(ctx, firecracker.Options{
		Binary:     cfg.Binary,
		SockPath:   filepath.Join(cfg.RunDir, id+".sock"),
		Console:    cfg.Console,
		ConsoleIn:  cfg.ConsoleIn,
		Cleanup:    cleanup,
		CgroupFD:   cgroupFD,
		Jail:       m.jail,
		JailHelper: cfg.JailHelper,
	})
	if err != nil {
		m.release()
		return nil, err
	}
	m.vmm, m.vsockPath = vmm, vsockPath

	// Any failure past this point leaves a live VMM behind, so unwind it.
	fail := func(err error) (*Machine, error) {
		_ = vmm.Shutdown(context.Background())
		m.release()
		return nil, err
	}

	c := vmm.Client()
	if err := c.SetBootSource(ctx, firecracker.BootSource{
		KernelImagePath: kernelPath,
		BootArgs:        BootArgs(cfg.Init, m.netArgs()),
	}); err != nil {
		return fail(err)
	}
	if err := c.SetDrive(ctx, firecracker.Drive{
		DriveID:      "rootfs",
		PathOnHost:   rootfsPath,
		IsRootDevice: true,
		// Read-only on purpose: the guest overlays a RAM upper over it, so it
		// never needs to write the disk — and a read-only image is one many
		// machines and forks can share without treading on each other.
		IsReadOnly: true,
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
		UDSPath:  vsockForVMM,
	}); err != nil {
		return fail(err)
	}
	if m.hasLease {
		if err := c.SetNetworkInterface(ctx, firecracker.NetworkInterface{
			IfaceID:     netIface,
			HostDevName: m.lease.Tap,
			GuestMAC:    m.lease.MAC,
		}); err != nil {
			return fail(err)
		}
	}
	if err := c.Start(ctx); err != nil {
		return fail(err)
	}

	// A machine can end without Destroy — the guest resets itself and the VMM
	// exits — so its host resources are released by whoever notices first. Only
	// worth a goroutine if there is something to release.
	if m.hasLease || m.cg != nil {
		go func() {
			<-vmm.Done()
			m.release()
		}()
	}

	return m, nil
}

// netArgs describes the machine's network to BootArgs, or nil when it has none.
func (m *Machine) netArgs() *NetArgs {
	if !m.hasLease {
		return nil
	}
	return &NetArgs{Lease: m.lease, DNS: m.cfg.DNS}
}

// NetArgs describes a machine's network to the guest kernel.
type NetArgs struct {
	Lease vmnet.Lease
	DNS   netip.Addr
}

// BootArgs builds the guest kernel command line.
//
//	console=ttyS0     kernel log and the guest's stdio land on the serial port
//	reboot=k          route guest resets through the i8042 controller, which is
//	                  what makes Firecracker exit when the guest reboots
//	panic=1           reboot on panic rather than hanging forever
//	pci=off           Firecracker exposes no PCI bus; skip probing for one
//
// The kernel boots into the overlay init, and the requested init rides along as
// eph.init= for the overlay init to exec once the RAM root is in place. A
// networked machine also gets ip=, handled below.
func BootArgs(init string, net *NetArgs) string {
	args := []string{"console=ttyS0", "reboot=k", "panic=1", "pci=off"}
	if init != "" {
		args = append(args, "init="+overlayInit, "eph.init="+init)
	}
	if net != nil {
		args = append(args, net.ipArg())
	}
	return strings.Join(args, " ")
}

// ipArg renders the kernel's IP autoconfiguration parameter.
//
// This is the whole of the guest's networking code, and it is not code: the
// kernel is built with CONFIG_IP_PNP=y, which means it configures the interface
// itself during boot, before init exists. No DHCP client, no dhcpcd, no shell
// script racing the interface — by the time PID 1 runs, the address is on and
// the default route is in place.
//
// The field order is fixed and mostly empty, which is what makes it look like a
// typo the first time you meet it:
//
//	ip=<client>:<server>:<gateway>:<netmask>:<hostname>:<device>:<autoconf>:<dns0>
//
// server is for NFS root, which we do not use, so it stays blank. autoconf=off
// means "these values are final, do not go looking for a DHCP server" — leaving
// it out makes the kernel try every autoconfiguration protocol it knows and
// spend several seconds failing.
//
// dns0 does not configure anything by itself; the kernel writes it to
// /proc/net/pnp, in the same format resolv.conf uses, and the guest's init
// copies it over. That is why the resolver is a boot argument here rather than
// a line in the image.
func (n *NetArgs) ipArg() string {
	return fmt.Sprintf("ip=%s::%s:%s:%s:%s:off:%s",
		n.Lease.Guest,
		n.Lease.Host,
		n.Lease.Netmask(),
		guestHostname,
		netIface,
		n.DNS,
	)
}

// Wait blocks until the machine exits. A guest that resets itself returns nil.
func (m *Machine) Wait() error { return m.vmm.Wait() }

// Done closes when the machine exits for any reason.
func (m *Machine) Done() <-chan struct{} { return m.vmm.Done() }

// Uptime reports how long the machine has been running.
func (m *Machine) Uptime() time.Duration { return time.Since(m.StartedAt) }

// Destroy tears the machine down and releases its runtime state.
func (m *Machine) Destroy(ctx context.Context) error {
	err := m.vmm.Shutdown(ctx)
	m.release()
	return err
}

// newID returns a short, collision-resistant machine identifier.
func newID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("machine: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
