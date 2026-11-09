package firecracker

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
)

// memAvailableMB reads MemAvailable from /proc/meminfo.
func memAvailableMB(t *testing.T) int {
	t.Helper()
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemAvailable:"); ok {
			kb, _ := strconv.Atoi(strings.Fields(v)[0])
			return kb / 1024
		}
	}
	t.Fatal("no MemAvailable")
	return 0
}

// pssMB is a process's proportional share of memory: pages shared with
// other clones count only their share.
func pssMB(pid int) float64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "Pss:"); ok {
			kb, _ := strconv.Atoi(strings.Fields(v)[0])
			return float64(kb) / 1024
		}
	}
	return 0
}

// TestDensity starts EPHEMERA_DENSITY sandboxes from a snapshot at once and
// reports start times and memory per sandbox. It is a measurement, run by
// hand: EPHEMERA_DENSITY=20 sudo -E go test -run TestDensity -v.
func TestDensity(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("EPHEMERA_DENSITY"))
	if n <= 0 {
		t.Skip("set EPHEMERA_DENSITY to the number of sandboxes")
	}
	be := jailedBackend(t)
	be.DefaultMemoryMB = 1024
	be.TemplateDir = filepath.Join(filepath.Dir(be.ImageDir), fmt.Sprintf("tmpl-density-%d", os.Getpid()))
	t.Cleanup(func() { os.RemoveAll(be.TemplateDir) })
	be.Snapshots = true
	ctx := context.Background()
	if err := be.Prepare(ctx); err != nil {
		t.Fatal(err)
	}

	before := memAvailableMB(t)
	ids := make([]string, n)
	durations := make([]time.Duration, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range ids {
		ids[i] = fmt.Sprintf("sbx_dense_%03d", i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := time.Now()
			errs[i] = be.Create(ctx, ids[i], api.Spec{})
			durations[i] = time.Since(s)
		}(i)
	}
	wg.Wait()
	all := time.Since(start)
	defer func() {
		for _, id := range ids {
			be.Destroy(ctx, id)
		}
	}()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("sandbox %d: %v", i, err)
		}
	}

	// Each sandbox does a little work, as an idle dev sandbox would.
	for _, id := range ids {
		run(t, be, id, "echo hi > /workspace/x && node -e 1")
	}
	time.Sleep(2 * time.Second)

	after := memAvailableMB(t)
	var pss float64
	for _, id := range ids {
		vm, _ := be.vm(id)
		pss += pssMB(vm.PID())
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	t.Logf("%d sandboxes started at once in %v (each: median %v, slowest %v)",
		n, all.Round(time.Millisecond), durations[n/2].Round(time.Millisecond), durations[n-1].Round(time.Millisecond))
	t.Logf("host memory used: %d MB in total, %.0f MB per sandbox (each has 1024 MB of guest memory)",
		before-after, float64(before-after)/float64(n))
	t.Logf("proportional memory (PSS) of the VMs: %.0f MB in total, %.0f MB per sandbox", pss, pss/float64(n))
	t.Logf("memory left: %d MB available", after)
}
