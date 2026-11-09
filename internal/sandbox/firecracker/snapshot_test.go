package firecracker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/worktree"
)

func run(t *testing.T, be *Backend, id, script string) string {
	t.Helper()
	var out bytes.Buffer
	code, err := be.Exec(context.Background(), id, api.ExecRequest{Argv: []string{"sh", "-c", script}}, &out, &out)
	if err != nil || code != 0 {
		t.Fatalf("%s: code %d err %v\n%s", id, code, err, out.String())
	}
	return out.String()
}

func testSnapshots(t *testing.T, be *Backend) {
	ctx := context.Background()
	var mu sync.Mutex
	hows := map[string]string{}
	took := map[string]time.Duration{}
	be.Log = func(id, how string, d time.Duration) {
		mu.Lock()
		hows[id], took[id] = how, d
		mu.Unlock()
	}

	// A cold boot, for comparison.
	be.Snapshots = false
	if err := be.Create(ctx, "sbx_cold", api.Spec{}); err != nil {
		t.Fatal(err)
	}
	be.Destroy(ctx, "sbx_cold")

	be.Snapshots = true
	start := time.Now()
	if err := be.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("template built in %v", time.Since(start).Round(time.Millisecond))

	ids := []string{"sbx_clone_a", "sbx_clone_b", "sbx_clone_c"}
	for _, id := range ids {
		if err := be.Create(ctx, id, api.Spec{}); err != nil {
			t.Fatal(err)
		}
		defer be.Destroy(ctx, id)
	}
	t.Logf("cold boot: %v", took["sbx_cold"].Round(time.Millisecond))
	for _, id := range ids {
		t.Logf("%s: %s in %v", id, hows[id], took[id].Round(time.Millisecond))
		if hows[id] != "restore" {
			t.Errorf("%s started by %q, want restore", id, hows[id])
		}
	}

	// The clock is right, not the snapshot's.
	now := time.Now().Unix()
	got, _ := strconv.ParseInt(strings.TrimSpace(run(t, be, ids[0], "date +%s")), 10, 64)
	if d := got - now; d < -5 || d > 5 {
		t.Errorf("guest clock is %ds off", d)
	}

	// Each clone has its own randomness.
	seen := map[string]string{}
	for _, id := range ids {
		r := strings.TrimSpace(run(t, be, id, "head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \\n'; echo; cat /proc/sys/kernel/random/uuid"))
		if other, dup := seen[r]; dup {
			t.Errorf("%s and %s produced the same random bytes: %s", id, other, r)
		}
		seen[r] = id
	}

	// Each clone has its own disk.
	run(t, be, ids[0], "echo mine > /root/only-in-a && touch /etc/changed-in-a")
	if out := run(t, be, ids[1], "ls /root/only-in-a /etc/changed-in-a 2>/dev/null || echo absent"); !strings.Contains(out, "absent") {
		t.Errorf("clone b sees clone a's files: %q", out)
	}

	// Files and commands work after a restore.
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "app.txt"), []byte("synced after restore\n"), 0o644)
	var tarball bytes.Buffer
	worktree.WriteTar(&tarball, src, []string{"app.txt"})
	if err := be.CopyIn(ctx, ids[2], &tarball); err != nil {
		t.Fatal(err)
	}
	if out := run(t, be, ids[2], "cat app.txt"); out != "synced after restore\n" {
		t.Errorf("upload after restore: %q", out)
	}
}

func TestSnapshotRestore(t *testing.T) {
	img := guestImage(t)
	be := &Backend{ImageDir: img, StateDir: t.TempDir(), TemplateDir: t.TempDir(), DefaultCPUs: 2, DefaultMemoryMB: 1024}
	testSnapshots(t, be)
}

func TestSnapshotRestoreJailed(t *testing.T) {
	be := jailedBackend(t)
	be.DefaultMemoryMB = 1024
	// The template must share a filesystem with the jails.
	be.TemplateDir = filepath.Join(filepath.Dir(be.ImageDir), fmt.Sprintf("tmpl-test-%d", os.Getpid()))
	t.Cleanup(func() { os.RemoveAll(be.TemplateDir) })
	testSnapshots(t, be)
}
