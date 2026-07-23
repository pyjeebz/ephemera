package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/machine"
)

// SnapshotRecord is a snapshot the daemon knows about: where its files are and
// what a fork needs to restore it. Unlike a machine record it describes no live
// process — a snapshot is just files on disk — so it persists until deleted
// rather than until something exits.
type SnapshotRecord struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"source_id"`
	StatePath string    `json:"state_path"`
	MemPath   string    `json:"mem_path"`
	BaseImage string    `json:"base_image"`
	Jailed    bool      `json:"jailed"`
	VCPUs     int       `json:"vcpus"`
	MemMiB    int       `json:"mem_mib"`
	CreatedAt time.Time `json:"created_at"`
}

// Snapshot converts a record back into the machine-layer descriptor a restore
// needs.
func (r SnapshotRecord) Snapshot() machine.Snapshot {
	return machine.Snapshot{
		SourceID:  r.SourceID,
		StatePath: r.StatePath,
		MemPath:   r.MemPath,
		BaseImage: r.BaseImage,
		Jailed:    r.Jailed,
		VCPUs:     r.VCPUs,
		MemMiB:    r.MemMiB,
	}
}

// SnapshotStore is the daemon's registry of snapshots. It is separate from the
// machine store because the two have opposite lifetimes: a machine record lives
// only while its process does, a snapshot record lives until it is deleted.
type SnapshotStore struct {
	dir string

	mu    sync.RWMutex
	snaps map[string]SnapshotRecord
}

// OpenSnapshots prepares a snapshot store backed by dir, loading any records a
// previous daemon left — snapshots outlive the daemon that took them.
func OpenSnapshots(dir string) (*SnapshotStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: prepare %s: %w", dir, err)
	}
	s := &SnapshotStore{dir: dir, snaps: make(map[string]SnapshotRecord)}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("store: scan %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var r SnapshotRecord
		buf, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil || json.Unmarshal(buf, &r) != nil {
			continue // a corrupt record is skipped, not fatal
		}
		s.snaps[r.ID] = r
	}
	return s, nil
}

// Dir is where snapshot files should be written, one subdirectory per snapshot.
func (s *SnapshotStore) Dir() string { return s.dir }

// Add records a snapshot under id and persists it.
func (s *SnapshotStore) Add(id string, snap machine.Snapshot) (SnapshotRecord, error) {
	r := SnapshotRecord{
		ID:        id,
		SourceID:  snap.SourceID,
		StatePath: snap.StatePath,
		MemPath:   snap.MemPath,
		BaseImage: snap.BaseImage,
		Jailed:    snap.Jailed,
		VCPUs:     snap.VCPUs,
		MemMiB:    snap.MemMiB,
		CreatedAt: time.Now(),
	}
	buf, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return SnapshotRecord{}, fmt.Errorf("store: encode snapshot %s: %w", id, err)
	}
	tmp := s.recordPath(id) + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return SnapshotRecord{}, fmt.Errorf("store: write snapshot %s: %w", id, err)
	}
	if err := os.Rename(tmp, s.recordPath(id)); err != nil {
		return SnapshotRecord{}, fmt.Errorf("store: commit snapshot %s: %w", id, err)
	}

	s.mu.Lock()
	s.snaps[id] = r
	s.mu.Unlock()
	return r, nil
}

// Get returns a snapshot record by id.
func (s *SnapshotStore) Get(id string) (SnapshotRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.snaps[id]
	if !ok {
		return SnapshotRecord{}, fmt.Errorf("%w: snapshot %s", ErrNotFound, id)
	}
	return r, nil
}

// List returns every snapshot record, newest first.
func (s *SnapshotStore) List() []SnapshotRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SnapshotRecord, 0, len(s.snaps))
	for _, r := range s.snaps {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Remove forgets a snapshot and deletes its files and record.
func (s *SnapshotStore) Remove(id string) error {
	s.mu.Lock()
	_, ok := s.snaps[id]
	delete(s.snaps, id)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: snapshot %s", ErrNotFound, id)
	}

	// The files live in a per-snapshot directory beside the record.
	_ = os.RemoveAll(filepath.Join(s.dir, id))
	if err := os.Remove(s.recordPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("store: remove snapshot record %s: %w", id, err)
	}
	return nil
}

func (s *SnapshotStore) recordPath(id string) string {
	return filepath.Join(s.dir, id+".json")
}
