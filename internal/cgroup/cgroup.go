// Package cgroup caps what a machine may spend on the host: CPU time and
// memory, enforced by the kernel through cgroup v2.
//
// The privilege story is the same shape as vmnet's. Creating cgroups under
// /sys/fs/cgroup needs root — but only once. host-setup.sh carves out a subtree
// (/sys/fs/cgroup/ephemera), enables the cpu and memory controllers for its
// children, and hands ownership to the user. After that the daemon makes one
// leaf cgroup per machine and writes its limits with no privilege at all,
// because it owns the subtree it is writing into.
//
// The VMM is placed into its cgroup at spawn time via CLONE_INTO_CGROUP (Go's
// SysProcAttr.UseCgroupFD), so it is inside the limit before it runs its first
// instruction — there is no window where a machine exists uncapped.
package cgroup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultRoot is the delegated subtree host-setup.sh prepares.
const DefaultRoot = "/sys/fs/cgroup/ephemera"

// memHeadroomMiB is added to a machine's RAM to get its memory limit.
//
// memory.max caps the VMM's *host* memory: the guest RAM it has faulted in, plus
// Firecracker's own footprint — code, heap, the page tables mapping guest
// physical memory. Setting the limit to exactly the guest's RAM would make the
// VMM OOM the moment the guest touched its last page. The headroom is slack for
// the overhead, generous on purpose: this is a backstop against a leak or a
// runaway, not a limit meant to bite in normal running.
const memHeadroomMiB = 64

// cpuPeriod is the accounting window for cpu.max, in microseconds. A machine may
// use vcpus × this much CPU time per window — i.e. its vCPU count in whole
// cores. The value is the usual default; only the quota carries meaning.
const cpuPeriod = 100_000

// Limits describe a machine's resource shape.
type Limits struct {
	MemMiB int
	VCPUs  int
}

// Manager owns a delegated cgroup subtree and makes one leaf per machine.
type Manager struct {
	root string
}

// New returns a manager over root, or DefaultRoot when root is empty. It does
// not check the subtree is usable — call Available for that.
func New(root string) *Manager {
	if root == "" {
		root = DefaultRoot
	}
	return &Manager{root: root}
}

// Root is the subtree this manager writes into.
func (m *Manager) Root() string { return m.root }

// Available reports whether the subtree is present, writable, and has the
// controllers a machine's limits need enabled for its children.
//
// The controllers question is the subtle one. A cgroup's own cpu.max/memory.max
// files appear only when its *parent* has those controllers in subtree_control.
// So it is the root's subtree_control we check, not its controllers list — the
// former is "what my children can be limited by", the latter merely "what I
// could enable".
func (m *Manager) Available() error {
	if fi, err := os.Stat(m.root); err != nil {
		return fmt.Errorf("cgroup: %s not present (run build/host-setup.sh): %w", m.root, err)
	} else if !fi.IsDir() {
		return fmt.Errorf("cgroup: %s is not a directory", m.root)
	}

	enabled, err := os.ReadFile(filepath.Join(m.root, "cgroup.subtree_control"))
	if err != nil {
		return fmt.Errorf("cgroup: read subtree_control: %w", err)
	}
	have := make(map[string]bool)
	for c := range strings.FieldsSeq(string(enabled)) {
		have[c] = true
	}
	for _, need := range []string{"cpu", "memory"} {
		if !have[need] {
			return fmt.Errorf("cgroup: %s does not delegate the %q controller to children (run build/host-setup.sh)", m.root, need)
		}
	}

	// A leaf we can create and remove proves ownership rather than inferring it
	// from directory permissions, which delegation makes fiddly to read.
	probe := filepath.Join(m.root, ".probe")
	if err := os.Mkdir(probe, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("cgroup: cannot create leaves under %s (run build/host-setup.sh): %w", m.root, err)
	}
	_ = os.Remove(probe)

	// Owning the subtree is necessary but not sufficient. Placing a process into
	// a leaf — whether by CLONE_INTO_CGROUP or a later write to cgroup.procs — is
	// a migration, and cgroup v2 requires write access to the cgroup.procs of the
	// *common ancestor* of the process's current cgroup and the destination.
	//
	// This daemon starts life outside the subtree (in whatever cgroup launched
	// it), so that common ancestor is the hierarchy root, whose cgroup.procs is
	// root-owned. Without write access there, every spawn fails with a bare
	// EACCES that reads like a firecracker problem. Check it here instead, so the
	// daemon can disable caps with an explanation rather than failing per-boot.
	//
	// The clean fix on a normal system is a systemd unit with Delegate=yes, which
	// starts the daemon already inside its scope and removes the boundary
	// crossing entirely; host-setup.sh grants the write directly for a box
	// without a systemd user session.
	ancestor := filepath.Join(filepath.Dir(m.root), "cgroup.procs")
	if err := unix.Access(ancestor, unix.W_OK); err != nil {
		return fmt.Errorf("cgroup: cannot place processes into %s: %s is not writable (run build/host-setup.sh): %w", m.root, ancestor, err)
	}
	return nil
}

// Cgroup is one machine's leaf: the directory the kernel enforces limits in, and
// an open descriptor to it for placing the VMM at spawn time.
type Cgroup struct {
	path string
	fd   int
}

// Create makes a machine's cgroup, writes its limits, and opens it for spawning.
//
// Order matters: the limits are written before anything is placed inside, so the
// VMM is never briefly resident in an unlimited cgroup.
func (m *Manager) Create(id string, lim Limits) (*Cgroup, error) {
	path := filepath.Join(m.root, id)
	if err := os.Mkdir(path, 0o755); err != nil {
		return nil, fmt.Errorf("cgroup: create %s: %w", path, err)
	}

	// Unwind the directory if we cannot finish configuring it — a half-made
	// cgroup with no limits is worse than none.
	fail := func(err error) (*Cgroup, error) {
		_ = os.Remove(path)
		return nil, err
	}

	memMax := int64(lim.MemMiB+memHeadroomMiB) << 20
	if err := writeFile(path, "memory.max", strconv.FormatInt(memMax, 10)); err != nil {
		return fail(err)
	}
	cpuMax := fmt.Sprintf("%d %d", lim.VCPUs*cpuPeriod, cpuPeriod)
	if err := writeFile(path, "cpu.max", cpuMax); err != nil {
		return fail(err)
	}

	// O_PATH would be enough to name it, but UseCgroupFD wants a directory fd it
	// can use with CLONE_INTO_CGROUP, so open it for real.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(fmt.Errorf("cgroup: open %s: %w", path, err))
	}
	return &Cgroup{path: path, fd: fd}, nil
}

// FD is the directory descriptor for SysProcAttr.CgroupFD.
func (c *Cgroup) FD() int { return c.fd }

// Path is the cgroup's directory, recorded so a restarted daemon can clean up a
// leaf whose machine it no longer supervises.
func (c *Cgroup) Path() string { return c.path }

// Destroy closes the descriptor and removes the cgroup.
//
// A cgroup can only be removed once empty, and it empties when its last process
// dies — so this must run after the VMM is gone, which every caller arranges. It
// is called on both the teardown and the process-exit paths, so it tolerates
// having already been done.
func (c *Cgroup) Destroy() error {
	if c.fd >= 0 {
		_ = unix.Close(c.fd)
		c.fd = -1
	}
	return remove(c.path)
}

// Remove deletes a cgroup by path, for cleaning up after a previous daemon.
func Remove(path string) error { return remove(path) }

// remove rmdirs a cgroup, retrying briefly. A just-killed VMM is removed from
// the cgroup only when the kernel reaps it, which can lag the kill by a moment;
// without the retry the rmdir races that and fails with EBUSY.
func remove(path string) error {
	var err error
	for range 50 {
		if err = os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if !errors.Is(err, unix.EBUSY) {
			return fmt.Errorf("cgroup: remove %s: %w", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("cgroup: remove %s: still busy after retrying: %w", path, err)
}

func writeFile(dir, name, val string) error {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(val), 0o644); err != nil {
		return fmt.Errorf("cgroup: write %s=%s: %w", name, val, err)
	}
	return nil
}
