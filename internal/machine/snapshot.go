package machine

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/pyjeebz/ephemera/internal/firecracker"
	"github.com/pyjeebz/ephemera/internal/jail"
	"golang.org/x/sys/unix"
)

// Snapshot is a machine frozen to disk: its device and vCPU state, its guest
// memory, and — for a jailed source — a frozen copy of its disk, plus enough to
// bring copies back.
//
// The point of the whole exercise is what the memory file contains: a guest that
// has already booted and is already running its agent. Restoring it skips the
// second of kernel-and-userland startup that a fresh Boot pays, so a machine
// comes back ready rather than starting.
//
// A jailed snapshot is the one that can be forked. Its guest listens on the
// in-jail path /run/vsock.sock, which resolves to a different host socket in
// every restore's own chroot — so many copies can run at once without colliding
// on it. A non-jailed snapshot stores an absolute host socket path and can only
// be restored one at a time.
type Snapshot struct {
	SourceID  string
	StatePath string // host path to the device/vCPU state
	MemPath   string // host path to the guest RAM image
	BaseImage string // host path to the read-only base disk, shared by every fork
	Jailed    bool
	VsockPath string // non-jailed only: the host socket a restore rebinds
	VCPUs     int
	MemMiB    int
}

// Snapshot pauses the machine and writes a full snapshot into dir. The machine
// is left paused; the caller resumes it to keep it running (the fork case, where
// the parent lives on) or destroys it.
//
// The guest must be paused first so its memory is a coherent instant and not a
// moving target while the image is written.
func (m *Machine) Snapshot(ctx context.Context, dir string) (Snapshot, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Snapshot{}, fmt.Errorf("machine: prepare snapshot dir: %w", err)
	}
	if err := m.vmm.Client().Pause(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("machine: pause for snapshot: %w", err)
	}
	if m.jail != nil {
		return m.snapshotJailed(ctx, dir)
	}
	return m.snapshotDirect(ctx, dir)
}

func (m *Machine) snapshotDirect(ctx context.Context, dir string) (Snapshot, error) {
	state := filepath.Join(dir, m.ID+".state")
	mem := filepath.Join(dir, m.ID+".mem")
	if err := m.vmm.Client().CreateSnapshot(ctx, firecracker.SnapshotCreate{
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

// snapshotJailed writes the snapshot inside the jail — where the confined VMM can
// reach the paths — then copies the state and memory out to dir so the snapshot
// outlives the machine that produced it.
//
// The disk is not captured. Because every machine runs on a read-only base with
// its writes in a RAM overlay, the base image is never modified, so a fork just
// shares it read-only. The snapshot records where it is; there is nothing to
// copy, which is what makes a fork cost only its memory.
func (m *Machine) snapshotJailed(ctx context.Context, dir string) (Snapshot, error) {
	// Firecracker writes to these paths inside its chroot; the host sees them
	// under the jail directory, because that directory is ordinary host storage.
	if err := m.vmm.Client().CreateSnapshot(ctx, firecracker.SnapshotCreate{
		SnapshotType: "Full",
		SnapshotPath: "/run/state",
		MemFilePath:  "/run/mem",
	}); err != nil {
		return Snapshot{}, fmt.Errorf("machine: create snapshot: %w", err)
	}

	jailDir, _ := m.Jailed()
	state := filepath.Join(dir, m.ID+".state")
	mem := filepath.Join(dir, m.ID+".mem")
	for _, c := range []struct{ from, to string }{
		{filepath.Join(jailDir, "run", "state"), state},
		{filepath.Join(jailDir, "run", "mem"), mem},
	} {
		if err := copyFile(c.from, c.to); err != nil {
			return Snapshot{}, fmt.Errorf("machine: capture snapshot: %w", err)
		}
	}

	return Snapshot{
		SourceID:  m.ID,
		StatePath: state,
		MemPath:   mem,
		BaseImage: m.cfg.RootfsPath, // shared, read-only, never copied
		Jailed:    true,
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
	Snapshot   Snapshot
	RunDir     string
	Binary     string // firecracker executable; empty looks it up on PATH
	JailHelper string // required for a jailed snapshot
}

// Restore brings a snapshot back to life in a fresh VMM and returns once the
// guest is running again. Because the agent was already up when the snapshot was
// taken, the returned machine is immediately ready — there is no WaitAgent to
// sit through, which is the entire value of doing this.
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
	if snap.Jailed {
		return restoreJailed(ctx, cfg)
	}
	return restoreDirect(ctx, cfg)
}

func restoreDirect(ctx context.Context, cfg RestoreConfig) (*Machine, error) {
	snap := cfg.Snapshot
	id, err := newID()
	if err != nil {
		return nil, err
	}
	runDir := orDefault(cfg.RunDir, "run")

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
	if err := loadAndResume(ctx, vmm, snap.StatePath, snap.MemPath); err != nil {
		_ = vmm.Shutdown(context.Background())
		return nil, err
	}
	return &Machine{
		ID:        id,
		StartedAt: time.Now(),
		cfg:       Config{VCPUs: snap.VCPUs, MemMiB: snap.MemMiB, RunDir: runDir},
		vmm:       vmm,
		vsockPath: snap.VsockPath,
	}, nil
}

// restoreJailed brings a snapshot up inside a fresh jail. This is the path fork
// uses: the jail gives the restored guest its own vsock socket, and the shared
// read-only base gives every copy the same disk with no copy at all — each fork
// keeps its own writes in the RAM overlay its restored memory already carries.
func restoreJailed(ctx context.Context, cfg RestoreConfig) (*Machine, error) {
	snap := cfg.Snapshot
	if cfg.JailHelper == "" {
		return nil, fmt.Errorf("machine: restoring a jailed snapshot needs a jail helper")
	}
	if _, err := os.Stat(snap.BaseImage); err != nil {
		return nil, fmt.Errorf("machine: snapshot base image not usable: %w", err)
	}

	fcBin := cfg.Binary
	if fcBin == "" {
		resolved, err := exec.LookPath(firecracker.DefaultBinary)
		if err != nil {
			return nil, fmt.Errorf("machine: locate firecracker: %w", err)
		}
		fcBin = resolved
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	runDir := orDefault(cfg.RunDir, "run")
	jailDir := filepath.Join(runDir, "jails", id)

	spec := &jail.Spec{
		Dir:    jail.Dir(jailDir),
		Binary: fcBin,
		// The read-only base is bound in, shared by every fork. Firecracker
		// reopens it read-only from the snapshot's device config, so no two forks
		// ever write it — and the guest's overlay keeps their filesystems apart.
		Rootfs:    snap.BaseImage,
		SnapState: snap.StatePath,
		SnapMem:   snap.MemPath,
		APISock:   "run/firecracker.sock",
		VsockSock: "run/vsock.sock",
	}
	vmm, err := firecracker.Launch(ctx, firecracker.Options{
		Binary:     fcBin,
		Jail:       spec,
		JailHelper: cfg.JailHelper,
	})
	if err != nil {
		return nil, err
	}
	// The snapshot is bound read-only inside the jail at fixed names.
	if err := loadAndResume(ctx, vmm, jail.GuestSnapState, jail.GuestSnapMem); err != nil {
		_ = vmm.Shutdown(context.Background())
		return nil, err
	}

	m := &Machine{
		ID:        id,
		StartedAt: time.Now(),
		cfg:       Config{VCPUs: snap.VCPUs, MemMiB: snap.MemMiB, RunDir: runDir},
		vmm:       vmm,
		vsockPath: spec.HostVsockSock(),
		jail:      spec,
	}
	return m, nil
}

// loadAndResume rebuilds the guest from a snapshot and starts it running.
func loadAndResume(ctx context.Context, vmm *firecracker.VMM, statePath, memPath string) error {
	if err := vmm.Client().LoadSnapshot(ctx, firecracker.SnapshotLoad{
		SnapshotPath: statePath,
		MemBackend:   firecracker.MemBackend{BackendType: "File", BackendPath: memPath},
		ResumeVM:     true,
	}); err != nil {
		return fmt.Errorf("machine: load snapshot: %w", err)
	}
	return nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// copyFile copies src to dst, skipping holes. A rootfs image is 1 GiB in name
// and ~50 MiB in fact — the rest is a hole — so copying it byte for byte would
// spend most of its time writing zeros. Copying only the data extents turns a
// multi-second copy into a fraction of a second, which is what keeps a fork
// closer to instant than to a reboot. On a filesystem without SEEK_DATA this
// falls back to a straight copy.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	if err := sparseCopy(in, out, info.Size()); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// sparseCopy writes only src's data regions to out, leaving the gaps as holes,
// then sets out to the full size so any trailing hole is preserved.
func sparseCopy(in, out *os.File, size int64) error {
	fd := int(in.Fd())
	var off int64
	for off < size {
		dataStart, err := unix.Seek(fd, off, unix.SEEK_DATA)
		if err != nil {
			if err == unix.ENXIO {
				break // no more data before the end; the rest is a hole
			}
			// SEEK_DATA unsupported here — fall back to a plain copy.
			return plainCopy(in, out)
		}
		holeStart, err := unix.Seek(fd, dataStart, unix.SEEK_HOLE)
		if err != nil {
			return err
		}
		if _, err := in.Seek(dataStart, io.SeekStart); err != nil {
			return err
		}
		if _, err := out.Seek(dataStart, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyN(out, in, holeStart-dataStart); err != nil {
			return err
		}
		off = holeStart
	}
	// Truncate to the real size so a hole at the very end is not lost.
	return out.Truncate(size)
}

func plainCopy(in, out *os.File) error {
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := io.Copy(out, in)
	return err
}
