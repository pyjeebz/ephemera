package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/record"
)

func TestFirecrackerProjectSnapshot(t *testing.T) {
	be := firecrackerBackend(t)
	be.ProjectSnapshotDir = t.TempDir()
	if be.Jailed {
		// Decrypted files move into the jail: keep them on its filesystem.
		be.ProjectSnapshotDir = filepath.Join(filepath.Dir(be.ImageDir), fmt.Sprintf("snaps-e2e-%d", os.Getpid()))
		t.Cleanup(func() { os.RemoveAll(be.ProjectSnapshotDir) })
	}
	s := newStack(t, be, "unused")
	dir := worktree(t, map[string]string{"app.js": "console.log(1)"})
	key := make([]byte, 32)
	rand.Read(key)
	snapID := strings.Repeat("c0ffee", 6) + "abcd"
	// A slow setup that leaves a build stamp and a secret on the disk.
	setup := func(k []byte) *client.Setup {
		return &client.Setup{SnapshotID: snapID, Key: k, Argv: []string{"sh", "-c",
			"sleep 3; mkdir -p node_modules/dep && date +%s%N > node_modules/dep/built && echo SECRET-MARKER-4711 > node_modules/dep/secret"}}
	}
	run := func(k []byte) (string, map[string]any, time.Duration, *api.SignedRecord) {
		t.Helper()
		var out bytes.Buffer
		var snapEv map[string]any
		start := time.Now()
		res, err := s.client.Run(context.Background(), client.RunOptions{
			Dir: dir, Setup: setup(k),
			Exec:   api.ExecRequest{Argv: []string{"cat", "node_modules/dep/built"}},
			Stdout: &out, Stderr: &out,
			Events: func(ev string, f map[string]any) {
				switch ev {
				case "snapshot":
					snapEv = f
				case "snapshot_error":
					t.Errorf("saving the snapshot failed: %v", f["error"])
				}
			},
		})
		if err != nil {
			t.Fatalf("run: %v\n%s", err, out.String())
		}
		return strings.TrimSpace(out.String()), snapEv, time.Since(start), res.Record
	}

	first, ev1, d1, rec1 := run(key)
	if ev1["loaded"] != false {
		t.Fatalf("first run loaded a snapshot: %v", ev1)
	}
	second, ev2, d2, rec2 := run(key)
	t.Logf("first run (setup, then snapshot): %v; second (from snapshot): %v", d1.Round(time.Millisecond), d2.Round(time.Millisecond))
	if ev2["loaded"] != true {
		t.Fatalf("second run did not load the snapshot: %v", ev2)
	}
	if second != first {
		t.Errorf("second run's build stamp %q, want %q from the snapshot", second, first)
	}
	if d2 >= d1-2*time.Second {
		t.Errorf("second run took %v; setup (3s) was not skipped", d2)
	}
	r1, _ := record.Verify(*rec1)
	r2, _ := record.Verify(*rec2)
	if r1.SnapshotSaved != snapID || r2.SnapshotLoaded != snapID {
		t.Errorf("records: saved %q, loaded %q", r1.SnapshotSaved, r2.SnapshotLoaded)
	}

	// At rest it is encrypted, and no plaintext is left about.
	snapDir := filepath.Join(be.ProjectSnapshotDir, snapID)
	filepath.Walk(be.ProjectSnapshotDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if strings.HasPrefix(info.Name(), ".save-") || !strings.HasSuffix(p, ".enc") && info.Name() != "meta.json" {
				t.Errorf("unexpected file in the snapshot store: %s", p)
			}
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("SECRET-MARKER-4711")) {
				t.Errorf("%s holds the setup's secret in plaintext", p)
			}
		}
		return nil
	})
	if left, _ := os.ReadDir(be.StateDir); len(left) != 0 {
		t.Errorf("left in the VM directory: %v", left)
	}

	// The wrong key loads nothing; setup runs again.
	other := make([]byte, 32)
	rand.Read(other)
	third, ev3, _, _ := run(other)
	if ev3["loaded"] != false || third == first {
		t.Errorf("wrong key: event %v, stamp %q", ev3, third)
	}
	// That run saved its own snapshot over the old one; run once more with
	// it, then tamper with a file: the snapshot is refused and removed.
	run(other)
	files, _ := filepath.Glob(filepath.Join(snapDir, "*.enc"))
	if len(files) == 0 {
		t.Fatal("no encrypted files")
	}
	b, _ := os.ReadFile(files[0])
	b[len(b)/2] ^= 0xff
	os.WriteFile(files[0], b, 0o600)
	_, ev5, _, _ := run(other)
	if ev5["loaded"] != false || !strings.Contains(fmt.Sprint(ev5["reason"]), "did not decrypt") {
		t.Errorf("tampered snapshot: %v", ev5)
	}
}
