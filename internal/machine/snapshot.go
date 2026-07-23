package machine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pyjeebz/ephemera/internal/firecracker"
)

// Snapshot is a machine frozen to disk: its device and vCPU state, its guest
// memory, and the little a restore needs to bring it back — chiefly the socket
// the in-guest agent listens on, which the restored machine inherits.
//
// The point of the whole exercise is what the memory file contains: a guest
// that has already booted and is already running its agent. Restoring it skips
// the second of kernel-and-userland startup that a fresh Boot pays, so a machine
// comes back ready rather than starting.
type Snapshot struct {
	SourceID  string
	StatePath string // host path to the device/vCPU state
	MemPath   string // host path to the guest RAM image
	VsockPath string // the agent socket the snapshot's vsock device expects
	VCPUs     int
	MemMiB    int
}

// Snapshot pauses the machine and writes a full snapshot into dir. The machine
// is left paused; the caller resumes it to keep it running, or destroys it —
// destroying is the common case, since the usual reason to snapshot is to throw
// the original away and restore copies later.
//
// The guest must be paused first so its memory is a coherent instant and not a
// moving target while the image is written.
func (m *Machine) Snapshot(ctx context.Context, dir string) (Snapshot, error) {
	if m.jail != nil {
		// A jailed VMM writes inside its chroot, so the snapshot paths would have
		// to be translated the way the boot images are. That composition comes
		// after the mechanism itself is proven; refuse clearly until then.
		return Snapshot{}, fmt.Errorf("machine: snapshotting a jailed machine is not supported yet")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Snapshot{}, fmt.Errorf("machine: prepare snapshot dir: %w", err)
	}

	c := m.vmm.Client()
	if err := c.Pause(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("machine: pause for snapshot: %w", err)
	}

	state := filepath.Join(dir, m.ID+".state")
	mem := filepath.Join(dir, m.ID+".mem")
	if err := c.CreateSnapshot(ctx, firecracker.SnapshotCreate{
		SnapshotType: "Full",
		SnapshotPath: state,
		MemFilePath:  mem,
	}); err != nil {
		return Snapshot{}, fmt.Errorf("machine: create snapshot: %w", err)
	}

	return Snapshot{
		SourceID:  m.ID,
		StatePath: state,
		MemPath:   mem,
		VsockPath: m.vsockPath,
		VCPUs:     m.cfg.VCPUs,
		MemMiB:    m.cfg.MemMiB,
	}, nil
}

// Resume unfreezes a machine paused by Snapshot.
func (m *Machine) Resume(ctx context.Context) error {
	return m.vmm.Client().Resume(ctx)
}

// RestoreConfig describes a restore.
type RestoreConfig struct {
	Snapshot Snapshot
	RunDir   string
	Binary   string // firecracker executable; empty looks it up on PATH
}

// Restore brings a snapshot back to life in a fresh VMM and returns once the
// guest is running again. Because the agent was already up when the snapshot was
// taken, the returned machine is immediately ready — there is no WaitAgent to
// sit through, which is the entire value of doing this.
//
// The restored guest listens on the same vsock socket the original did, so the
// original must be gone first; Restore clears any stale socket at that path
// before the new VMM can bind it.
func Restore(ctx context.Context, cfg RestoreConfig) (*Machine, error) {
	snap := cfg.Snapshot
	for _, f := range []struct{ name, path string }{
		{"state", snap.StatePath},
		{"memory", snap.MemPath},
	} {
		if _, err := os.Stat(f.path); err != nil {
			return nil, fmt.Errorf("machine: snapshot %s not usable: %w", f.name, err)
		}
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}

	runDir := cfg.RunDir
	if runDir == "" {
		runDir = "run"
	}
	// The restored guest's vsock device binds the path stored in the snapshot, so
	// a leftover socket from the source machine would stop the new VMM binding it.
	if err := os.Remove(snap.VsockPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("machine: clear stale vsock socket: %w", err)
	}

	vmm, err := firecracker.Launch(ctx, firecracker.Options{
		Binary:   cfg.Binary,
		SockPath: filepath.Join(runDir, id+".sock"),
		Cleanup:  []string{snap.VsockPath},
	})
	if err != nil {
		return nil, err
	}

	if err := vmm.Client().LoadSnapshot(ctx, firecracker.SnapshotLoad{
		SnapshotPath: snap.StatePath,
		MemBackend:   firecracker.MemBackend{BackendType: "File", BackendPath: snap.MemPath},
		ResumeVM:     true,
	}); err != nil {
		_ = vmm.Shutdown(context.Background())
		return nil, fmt.Errorf("machine: load snapshot: %w", err)
	}

	return &Machine{
		ID:        id,
		StartedAt: time.Now(),
		cfg:       Config{VCPUs: snap.VCPUs, MemMiB: snap.MemMiB, RunDir: runDir},
		vmm:       vmm,
		vsockPath: snap.VsockPath,
	}, nil
}
