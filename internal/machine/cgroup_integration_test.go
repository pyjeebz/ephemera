package machine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/cgroup"
)

// Boots a real machine under a real cgroup and checks the host side: the limits
// are written, and the VMM actually landed inside the leaf. It needs the
// delegated subtree host-setup.sh prepares, so it skips without it.
//
// It proves placement and values, not enforcement — watching a guest get
// OOM-killed for exceeding its cap is a live check, noted in the buildlog, not
// something to provoke in the unit suite.
func TestMachineRunsUnderItsCgroup(t *testing.T) {
	cfg := requireVM(t)

	mgr := cgroup.New(cgroup.DefaultRoot)
	if err := mgr.Available(); err != nil {
		t.Skipf("cgroup subtree not available: %v", err)
	}
	cfg.Init = AgentInit
	cfg.Cgroup = mgr
	cfg.MemMiB = 256
	cfg.VCPUs = 1

	var console bytes.Buffer
	cfg.Console = &console

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() { _ = m.Destroy(context.Background()) }()

	cgPath := m.CgroupPath()
	if cgPath == "" {
		t.Fatal("machine reported no cgroup path")
	}

	t.Run("the VMM landed inside the leaf", func(t *testing.T) {
		// The point of CLONE_INTO_CGROUP: the VMM is a member from birth, so its
		// pid is in the leaf's cgroup.procs without anyone having moved it.
		procs := readCg(t, cgPath, "cgroup.procs")
		want := strconv.Itoa(m.Pid())
		found := false
		for line := range strings.FieldsSeq(procs) {
			if line == want {
				found = true
			}
		}
		if !found {
			t.Errorf("VMM pid %s not in cgroup.procs:\n%s", want, procs)
		}
	})

	t.Run("memory is capped with headroom", func(t *testing.T) {
		got := strings.TrimSpace(readCg(t, cgPath, "memory.max"))
		if got == "max" {
			t.Fatal("memory.max is unlimited")
		}
		v, err := strconv.ParseInt(got, 10, 64)
		if err != nil {
			t.Fatalf("memory.max = %q: %v", got, err)
		}
		// 256 MiB of guest RAM plus the fixed headroom, and nothing wildly more.
		if v <= 256<<20 {
			t.Errorf("memory.max = %d, expected above the 256 MiB guest RAM", v)
		}
		if v > 512<<20 {
			t.Errorf("memory.max = %d, far more than guest RAM + headroom", v)
		}
	})

	t.Run("cpu is capped to the vCPU count", func(t *testing.T) {
		if got := strings.TrimSpace(readCg(t, cgPath, "cpu.max")); got != "100000 100000" {
			t.Errorf("cpu.max = %q, want one core for a 1-vCPU machine", got)
		}
	})
}

func TestCgroupIsRemovedWithTheMachine(t *testing.T) {
	cfg := requireVM(t)

	mgr := cgroup.New(cgroup.DefaultRoot)
	if err := mgr.Available(); err != nil {
		t.Skipf("cgroup subtree not available: %v", err)
	}
	cfg.Init = AgentInit
	cfg.Cgroup = mgr
	cfg.Console = &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	cgPath := m.CgroupPath()

	if err := m.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(cgPath); !os.IsNotExist(err) {
		t.Errorf("cgroup %s survived Destroy", filepath.Base(cgPath))
	}
}

func readCg(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
