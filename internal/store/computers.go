package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// A computer is a named, persistent machine — the "use it like a laptop" idea.
// Its identity is a name and a writable disk that outlives any particular boot;
// starting it boots a machine on that disk, stopping it throws the machine away
// but keeps the disk, so the state is there next time.
//
// This is separate from the machine store for the same reason snapshots are: a
// machine record lives only while its process does, but a computer exists as
// long as its disk does, running or not.

// validName keeps a computer name usable as a filename and an id: letters,
// digits, dash, underscore.
var validName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ComputerRecord is a persistent computer the daemon knows about.
type ComputerRecord struct {
	Name      string    `json:"name"`
	DiskPath  string    `json:"disk_path"`
	CreatedAt time.Time `json:"created_at"`
}

// ComputerStore is the daemon's registry of persistent computers.
type ComputerStore struct {
	dir string

	mu        sync.RWMutex
	computers map[string]ComputerRecord
}

// OpenComputers prepares a computer store backed by dir, loading the records a
// previous daemon left — computers outlive daemons.
func OpenComputers(dir string) (*ComputerStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: prepare %s: %w", dir, err)
	}
	s := &ComputerStore{dir: dir, computers: make(map[string]ComputerRecord)}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("store: scan %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var r ComputerRecord
		buf, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil || json.Unmarshal(buf, &r) != nil {
			continue
		}
		s.computers[r.Name] = r
	}
	return s, nil
}

// DiskPath is where a computer's disk lives, one image per name.
func (s *ComputerStore) DiskPath(name string) string {
	return filepath.Join(s.dir, name+".ext4")
}

// Create records a new computer. The caller creates the disk itself (formatting
// is a machine-layer concern); this just remembers it. It fails if the name is
// invalid or already taken.
func (s *ComputerStore) Create(name, diskPath string) (ComputerRecord, error) {
	if !validName.MatchString(name) {
		return ComputerRecord{}, fmt.Errorf("store: invalid computer name %q (use letters, digits, - or _)", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.computers[name]; ok {
		return ComputerRecord{}, fmt.Errorf("store: computer %q already exists", name)
	}

	r := ComputerRecord{Name: name, DiskPath: diskPath, CreatedAt: time.Now()}
	if err := s.write(r); err != nil {
		return ComputerRecord{}, err
	}
	s.computers[name] = r
	return r, nil
}

// Get returns a computer by name.
func (s *ComputerStore) Get(name string) (ComputerRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.computers[name]
	if !ok {
		return ComputerRecord{}, fmt.Errorf("%w: computer %s", ErrNotFound, name)
	}
	return r, nil
}

// List returns every computer, oldest first.
func (s *ComputerStore) List() []ComputerRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ComputerRecord, 0, len(s.computers))
	for _, r := range s.computers {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Remove forgets a computer and deletes its disk and record. The caller must
// stop any running machine on it first.
func (s *ComputerStore) Remove(name string) error {
	s.mu.Lock()
	r, ok := s.computers[name]
	delete(s.computers, name)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: computer %s", ErrNotFound, name)
	}

	if r.DiskPath != "" {
		_ = os.Remove(r.DiskPath)
	}
	if err := os.Remove(s.recordPath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("store: remove computer record %s: %w", name, err)
	}
	return nil
}

func (s *ComputerStore) write(r ComputerRecord) error {
	buf, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode computer %s: %w", r.Name, err)
	}
	tmp := s.recordPath(r.Name) + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return fmt.Errorf("store: write computer %s: %w", r.Name, err)
	}
	if err := os.Rename(tmp, s.recordPath(r.Name)); err != nil {
		return fmt.Errorf("store: commit computer %s: %w", r.Name, err)
	}
	return nil
}

func (s *ComputerStore) recordPath(name string) string {
	return filepath.Join(s.dir, name+".json")
}
