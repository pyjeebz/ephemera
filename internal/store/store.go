// Package store tracks the machines a daemon is responsible for.
//
// It keeps two views that must not drift: the live machines this process
// supervises, and a record on disk for each one. The records exist for a single
// purpose — after a crash or restart, the VMMs are still running but nothing in
// this process knows about them, and only a durable note can find them again.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/machine"
)

// ErrNotFound is returned for an unknown machine id.
var ErrNotFound = errors.New("machine not found")

// Record is the durable description of a machine: enough to identify its VMM
// and clean up after it without this process having spawned it.
type Record struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	APISock   string    `json:"api_sock"`
	VsockPath string    `json:"vsock_path"`
	VCPUs     int       `json:"vcpus"`
	MemMiB    int       `json:"mem_mib"`
	StartedAt time.Time `json:"started_at"`
}

// Store is the daemon's machine registry.
type Store struct {
	dir string

	mu      sync.RWMutex
	live    map[string]*machine.Machine
	records map[string]Record
}

// Open prepares a store backed by dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: prepare %s: %w", dir, err)
	}
	return &Store{
		dir:     dir,
		live:    make(map[string]*machine.Machine),
		records: make(map[string]Record),
	}, nil
}

// Add registers a machine and writes its record before returning, so a crash
// immediately after this call still leaves something to reap.
func (s *Store) Add(m *machine.Machine, r Record) error {
	if err := s.writeRecord(r); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live[r.ID] = m
	s.records[r.ID] = r
	return nil
}

// Get returns a live machine by id.
func (s *Store) Get(id string) (*machine.Machine, Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.live[id]
	if !ok {
		return nil, Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return m, s.records[id], nil
}

// List returns the records of all live machines, oldest first.
func (s *Store) List() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	// Stable, meaningful order for a CLI listing.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartedAt.Before(out[j-1].StartedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Remove forgets a machine and deletes its record. It does not stop anything —
// callers destroy the machine first.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	delete(s.live, id)
	delete(s.records, id)
	s.mu.Unlock()

	if err := os.Remove(s.recordPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("store: remove record %s: %w", id, err)
	}
	return nil
}

func (s *Store) recordPath(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) writeRecord(r Record) error {
	buf, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode record %s: %w", r.ID, err)
	}
	// Write-then-rename so a reader never sees a half-written record.
	tmp := s.recordPath(r.ID) + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return fmt.Errorf("store: write record %s: %w", r.ID, err)
	}
	if err := os.Rename(tmp, s.recordPath(r.ID)); err != nil {
		return fmt.Errorf("store: commit record %s: %w", r.ID, err)
	}
	return nil
}

// Reap cleans up machines left behind by a previous daemon.
//
// A VMM cannot be re-adopted: supervising a process means owning its wait
// status, and a process we did not spawn cannot be waited on. So machines that
// outlived their daemon are destroyed rather than resumed. That is the honest
// behaviour for a sandbox that is ephemeral by design, but it does mean
// restarting the daemon takes running machines with it.
func Reap(dir string, log *slog.Logger) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("store: scan %s: %w", dir, err)
	}

	reaped := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())

		var r Record
		buf, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(buf, &r) != nil {
			// An unreadable record is itself garbage; drop it.
			log.Warn("discarding unreadable machine record", "path", path)
			_ = os.Remove(path)
			continue
		}

		if alive(r.PID) {
			log.Warn("destroying orphaned machine", "id", r.ID, "pid", r.PID)
			if err := syscall.Kill(r.PID, syscall.SIGKILL); err != nil {
				log.Error("could not kill orphan", "id", r.ID, "pid", r.PID, "err", err)
			}
			reaped++
		}
		for _, sock := range []string{r.APISock, r.VsockPath} {
			if sock != "" {
				_ = os.Remove(sock)
			}
		}
		_ = os.Remove(path)
	}
	return reaped, nil
}

// alive reports whether a pid is still running. Signal 0 performs the existence
// and permission checks without delivering anything.
//
// This can be fooled by pid reuse: if the recorded pid was recycled by an
// unrelated process, we would kill the wrong one. Firecracker VMMs are
// short-lived and pids are wide, so the window is small — but it is a real
// caveat, and the fix is a pidfd, which is worth doing when this grows up.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
