package cgroup

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The kernel's cgroupfs and an ordinary directory behave identically for
// everything this package does that is not enforcement — mkdir, write the limit
// files, open the dir, remove it. So the tests point the manager at a tmpdir and
// pin the *values written*, which is where the bugs would be. Actual enforcement
// (a guest that exceeds its cap gets killed) is verified live; see the buildlog.

func TestCreateWritesTheLimitFiles(t *testing.T) {
	root := t.TempDir()
	m := New(root)

	cg, err := m.Create("0f8bdebe", Limits{MemMiB: 256, VCPUs: 2})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer cg.Destroy()

	// memory.max = (RAM + headroom) in bytes. The headroom is what stops the VMM
	// from OOMing the instant the guest fills its RAM, so it must be included.
	wantMem := int64(256+memHeadroomMiB) << 20
	if got := readInt(t, cg.Path(), "memory.max"); got != wantMem {
		t.Errorf("memory.max = %d, want %d (256 MiB + %d headroom)", got, wantMem, memHeadroomMiB)
	}

	// cpu.max = "quota period"; quota = vcpus × period gives that many cores.
	if got := readFile(t, cg.Path(), "cpu.max"); got != "200000 100000" {
		t.Errorf("cpu.max = %q, want %q (2 vCPUs)", got, "200000 100000")
	}
}

func TestCreateScalesWithTheMachine(t *testing.T) {
	m := New(t.TempDir())

	cg, err := m.Create("aaaaaaaa", Limits{MemMiB: 1024, VCPUs: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer cg.Destroy()

	if got := readInt(t, cg.Path(), "memory.max"); got != int64(1024+memHeadroomMiB)<<20 {
		t.Errorf("memory.max = %d for a 1 GiB machine", got)
	}
	if got := readFile(t, cg.Path(), "cpu.max"); got != "100000 100000" {
		t.Errorf("cpu.max = %q, want one core", got)
	}
}

func TestDestroyRemovesTheCgroup(t *testing.T) {
	m := New(t.TempDir())
	cg, err := m.Create("deadbeef", Limits{MemMiB: 128, VCPUs: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// On real cgroupfs the control files are kernel-synthesized and rmdir removes
	// the cgroup regardless of them. A tmpdir has real files that would block the
	// rmdir, so clear them here to model the state rmdir actually sees — an empty
	// directory. Destroy itself does no such thing, and must not: unlinking a
	// control file on real cgroupfs is refused, and rmdir does not need it.
	for _, f := range []string{"memory.max", "cpu.max"} {
		if err := os.Remove(filepath.Join(cg.Path(), f)); err != nil {
			t.Fatal(err)
		}
	}

	if err := cg.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(cg.Path()); !os.IsNotExist(err) {
		t.Errorf("cgroup dir survived Destroy: %v", err)
	}
	// Destroy runs on more than one teardown path, so a second call must be a
	// no-op rather than an error.
	if err := cg.Destroy(); err != nil {
		t.Errorf("second Destroy: %v", err)
	}
}

func TestCreateUnwindsOnFailure(t *testing.T) {
	// A root that does not exist makes the mkdir fail; nothing should be left.
	m := New(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := m.Create("aaaaaaaa", Limits{MemMiB: 128, VCPUs: 1}); err == nil {
		t.Fatal("Create under a missing root returned no error")
	}
}

func TestAvailableRejectsAMissingSubtree(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "absent"))
	if err := m.Available(); err == nil {
		t.Fatal("Available accepted a subtree that does not exist")
	}
}

func TestAvailableRequiresTheControllersDelegated(t *testing.T) {
	// The subtree lives one level down so its parent can hold the cgroup.procs
	// whose writability Available checks, mirroring the real layout where the
	// subtree sits under the hierarchy root.
	parent := t.TempDir()
	root := filepath.Join(parent, "ephemera")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A writable ancestor cgroup.procs stands in for the placement grant.
	if err := os.WriteFile(filepath.Join(parent, "cgroup.procs"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(root)

	// A subtree that exists but delegates nothing to its children: a machine's
	// cpu.max/memory.max would never appear, so this must be refused.
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("io\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Available(); err == nil {
		t.Fatal("Available accepted a subtree with neither cpu nor memory delegated")
	}

	// With cpu and memory delegated it should pass — mkdir works in a tmpdir,
	// standing in for the ownership the real subtree would carry.
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("cpu memory pids\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Available(); err != nil {
		t.Errorf("Available rejected a properly delegated subtree: %v", err)
	}
}

func TestAvailableRequiresProcessPlacement(t *testing.T) {
	// Subtree owned and controllers delegated, but the ancestor cgroup.procs is
	// missing — the placement grant host-setup adds. Available must refuse, since
	// leaves would be creatable but nothing could be spawned into them.
	parent := t.TempDir()
	root := filepath.Join(parent, "ephemera")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("cpu memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := New(root).Available(); err == nil {
		t.Fatal("Available accepted a subtree with no way to place processes")
	}
}

func TestNewDefaultsToTheStandardRoot(t *testing.T) {
	if New("").Root() != DefaultRoot {
		t.Errorf("New(\"\").Root() = %q, want %q", New("").Root(), DefaultRoot)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func readInt(t *testing.T, dir, name string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(readFile(t, dir, name), 10, 64)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return v
}
