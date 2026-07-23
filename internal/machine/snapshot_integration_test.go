package machine

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This boots a machine, snapshots it, throws it away, and brings it back — the
// foundation the fork and warm-pool work stands on. It proves the restored guest
// is the *same running instance*, not a fresh boot: a marker written into guest
// memory survives, and the agent answers immediately, with no boot to wait out.
func TestSnapshotRestore(t *testing.T) {
	cfg := requireVM(t)
	cfg.Init = AgentInit
	cfg.Console = &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if err := m.WaitAgent(ctx); err != nil {
		t.Fatalf("agent never came up: %v", err)
	}

	// Write a marker into the running guest. /tmp aside, the surer proof is
	// PID 1's own age: the agent's start time cannot survive a reboot, so if it
	// is unchanged after restore, the guest was resumed rather than restarted.
	if _, err := m.Exec(ctx, []string{"sh", "-c", "echo alive-42 > /run/marker"}, nil, nil); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	var before bytes.Buffer
	if _, err := m.Exec(ctx, []string{"cat", "/proc/1/stat"}, &before, nil); err != nil {
		t.Fatalf("read pid1 stat: %v", err)
	}

	snapDir := t.TempDir()
	snap, err := m.Snapshot(ctx, snapDir)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := m.Destroy(ctx); err != nil {
		t.Fatalf("Destroy original: %v", err)
	}

	started := time.Now()
	r, err := Restore(ctx, RestoreConfig{Snapshot: snap, RunDir: cfg.RunDir, Binary: cfg.Binary})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restoreDur := time.Since(started)
	defer func() { _ = r.Destroy(context.Background()) }()

	t.Run("the agent answers with no boot wait", func(t *testing.T) {
		// No WaitAgent: a restored machine is supposed to be ready the instant
		// Restore returns. If the agent needed a boot, this exec would hang.
		var out bytes.Buffer
		if _, err := r.Exec(ctx, []string{"cat", "/run/marker"}, &out, nil); err != nil {
			t.Fatalf("Exec on restored machine: %v", err)
		}
		if strings.TrimSpace(out.String()) != "alive-42" {
			t.Errorf("marker = %q, want alive-42 — restored guest is not the snapshotted one", out.String())
		}
	})

	t.Run("it is a resume, not a reboot", func(t *testing.T) {
		var after bytes.Buffer
		if _, err := r.Exec(ctx, []string{"cat", "/proc/1/stat"}, &after, nil); err != nil {
			t.Fatalf("read pid1 stat after restore: %v", err)
		}
		// Field 22 of /proc/pid/stat is starttime, in clock ticks since boot. If
		// the guest had rebooted, its whole timeline resets and this changes.
		if startTime(before.String()) != startTime(after.String()) {
			t.Errorf("pid 1 start time changed across restore (%s -> %s): the guest rebooted, it did not resume",
				startTime(before.String()), startTime(after.String()))
		}
	})

	// Restore should be a small fraction of a boot. Not asserted as a hard bound
	// (CI machines vary), but logged so the win is visible and a regression shows.
	t.Logf("restored in %s (a cold boot is ~1s)", restoreDur.Round(time.Millisecond))
}

func TestRestoreRejectsAMissingSnapshot(t *testing.T) {
	_, err := Restore(context.Background(), RestoreConfig{
		Snapshot: Snapshot{
			StatePath: filepath.Join(t.TempDir(), "absent.state"),
			MemPath:   filepath.Join(t.TempDir(), "absent.mem"),
			VsockPath: filepath.Join(t.TempDir(), "x.vsock"),
		},
	})
	if err == nil {
		t.Fatal("Restore accepted a snapshot whose files do not exist")
	}
}

// startTime returns field 22 of a /proc/pid/stat line. The comm field (2) is
// parenthesised and may contain spaces, so count from the closing paren.
func startTime(stat string) string {
	i := strings.LastIndex(stat, ")")
	if i < 0 {
		return ""
	}
	fields := strings.Fields(stat[i+1:])
	// After ")" the fields are 3..N; starttime is field 22, i.e. index 22-3.
	const idx = 22 - 3
	if len(fields) <= idx {
		return ""
	}
	return fields[idx]
}
