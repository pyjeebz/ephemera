package store

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAddPersistsRecordBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	rec := Record{ID: "abc123", PID: 4242, VCPUs: 2, MemMiB: 512, StartedAt: time.Now()}
	if err := s.Add(nil, rec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// The whole point of the record is that it survives this process, so it has
	// to be on disk by the time Add returns.
	buf, err := os.ReadFile(filepath.Join(dir, "abc123.json"))
	if err != nil {
		t.Fatalf("record not written: %v", err)
	}
	var got Record
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("record is not valid json: %v", err)
	}
	if got.ID != rec.ID || got.PID != rec.PID || got.MemMiB != rec.MemMiB {
		t.Errorf("record round-trip mismatch: got %+v want %+v", got, rec)
	}
}

func TestAddLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := s.Add(nil, Record{ID: "x", StartedAt: time.Now()}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("write-then-rename left %s behind", e.Name())
		}
	}
}

func TestGetUnknownIsNotFound(t *testing.T) {
	s, _ := Open(t.TempDir())
	_, _, err := s.Get("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestListIsOldestFirst(t *testing.T) {
	s, _ := Open(t.TempDir())
	now := time.Now()
	// Added out of order on purpose.
	for _, r := range []Record{
		{ID: "third", StartedAt: now},
		{ID: "first", StartedAt: now.Add(-2 * time.Hour)},
		{ID: "second", StartedAt: now.Add(-1 * time.Hour)},
	} {
		if err := s.Add(nil, r); err != nil {
			t.Fatalf("Add %s: %v", r.ID, err)
		}
	}

	want := []string{"first", "second", "third"}
	got := s.List()
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Errorf("position %d: got %s, want %s", i, got[i].ID, want[i])
		}
	}
}

func TestRemoveClearsMemoryAndDisk(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Add(nil, Record{ID: "gone", StartedAt: time.Now()})

	if err := s.Remove("gone"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, _, err := s.Get("gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("still in memory after Remove")
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.json")); !os.IsNotExist(err) {
		t.Errorf("record still on disk after Remove")
	}
	if len(s.List()) != 0 {
		t.Errorf("List still reports the removed machine")
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.Add(nil, Record{ID: "x", StartedAt: time.Now()})
	if err := s.Remove("x"); err != nil {
		t.Fatalf("first Remove: %v", err)
	}
	// A machine that exited on its own may be removed twice; that is not an error.
	if err := s.Remove("x"); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

// writeRecordFile drops a record straight onto disk, standing in for one left
// behind by a daemon that died.
func writeRecordFile(t *testing.T, dir string, r Record) string {
	t.Helper()
	buf, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, r.ID+".json")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReapKillsALiveOrphan(t *testing.T) {
	dir := t.TempDir()

	// A stand-in for an abandoned VMM: a real process that will not exit on its
	// own within the test's lifetime.
	victim := exec.Command("sleep", "300")
	if err := victim.Start(); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	t.Cleanup(func() {
		_ = victim.Process.Kill()
		_ = victim.Wait()
	})

	sock := filepath.Join(dir, "orphan.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeRecordFile(t, dir, Record{ID: "orphan", PID: victim.Process.Pid, APISock: sock})

	n, err := Reap(dir, discardLogger())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if n != 1 {
		t.Errorf("reaped %d, want 1", n)
	}

	// Reap only signals; the process is gone once it has been waited on.
	_ = victim.Wait()
	if syscall.Kill(victim.Process.Pid, 0) == nil {
		t.Errorf("orphan pid %d still alive after Reap", victim.Process.Pid)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("record survived Reap")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("stale socket survived Reap")
	}
}

func TestReapDropsRecordsForDeadProcesses(t *testing.T) {
	dir := t.TempDir()

	// Run something to completion so its pid is a real one that is no longer alive.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	path := writeRecordFile(t, dir, Record{ID: "dead", PID: dead.ProcessState.Pid()})

	n, err := Reap(dir, discardLogger())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	// Nothing was killed, but the stale record must still be cleared.
	if n != 0 {
		t.Errorf("reaped %d, want 0 — nothing was running", n)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stale record survived Reap")
	}
}

func TestReapDiscardsUnreadableRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Reap(dir, discardLogger()); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("unreadable record survived Reap")
	}
}

func TestReapIgnoresNonRecords(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Reap(dir, discardLogger()); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("Reap deleted a file that is not a record: %v", err)
	}
}

func TestReapOnMissingDirIsNotAnError(t *testing.T) {
	n, err := Reap(filepath.Join(t.TempDir(), "never-created"), discardLogger())
	if err != nil {
		t.Fatalf("Reap on missing dir: %v", err)
	}
	if n != 0 {
		t.Errorf("reaped %d from a missing dir", n)
	}
}

func TestAliveRejectsNonsensePids(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if alive(pid) {
			t.Errorf("alive(%d) = true", pid)
		}
	}
}
