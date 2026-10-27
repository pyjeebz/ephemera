// Package upload is the relay's disk store for background uploads:
// the whole file, sent by the daemon in order after it announces, so
// the link keeps working once the sender goes offline. Each share is
// two files in the store's directory: <id>.data, the bytes so far,
// and <id>.json, what the link is and when it expires. Both are
// deleted at expiry or kill. They survive a relay restart, so an
// upload resumes where it stopped and a finished one stays served.
package upload

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ephemera.run/ephemera/internal/metrics"
)

// ErrFull is returned by Reserve when the file does not fit in what
// is left of the store's byte budget.
var ErrFull = errors.New("upload: store is full")

// Meta is what the relay needs to put a stored share back in its
// share table after a restart.
type Meta struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	MIME    string    `json:"mime"`
	Account string    `json:"account,omitempty"`
	Expires time.Time `json:"expires"`
}

// Store holds one growing file per share under a byte budget. The
// budget counts each share's full size from the moment it's
// reserved, so an accepted upload never runs out of room halfway.
type Store struct {
	dir    string
	budget int64

	mu       sync.Mutex
	entries  map[string]*entry
	reserved int64 // sum of Size over entries
}

type entry struct {
	meta Meta
	f    *os.File

	mu   sync.Mutex // serializes appends; guards have
	have int64      // bytes stored, all from offset 0
}

// Open loads the store in dir, creating it if needed. Expired shares,
// half-written metadata and data files without metadata are deleted.
// budget <= 0 disables the store: Reserve always fails.
func Open(dir string, budget int64) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, budget: budget, entries: make(map[string]*entry)}
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, de := range names {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		e, err := s.load(id)
		if err != nil || !e.meta.Expires.After(now) {
			if e != nil {
				e.f.Close()
			}
			s.remove(id)
			continue
		}
		s.entries[id] = e
		s.reserved += e.meta.Size
	}
	// anything left without a loaded entry is debris: a data file
	// whose metadata never landed, or a temp file from a crash
	for _, de := range names {
		name := de.Name()
		id := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".tmp"), ".json"), ".data")
		if _, ok := s.entries[id]; !ok && !de.IsDir() {
			os.Remove(filepath.Join(dir, name))
		}
	}
	metrics.UploadBytesHeld.Set(s.held())
	return s, nil
}

// load reads one share's metadata and opens its data file.
func (s *Store) load(id string) (*entry, error) {
	if !validID(id) {
		return nil, fmt.Errorf("upload: bad id %q", id)
	}
	b, err := os.ReadFile(s.path(id, ".json"))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil || m.ID != id || m.Size < 1 {
		return nil, fmt.Errorf("upload: bad metadata for %s", id)
	}
	f, err := os.OpenFile(s.path(id, ".data"), os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	have := st.Size()
	if have > m.Size {
		// never serve past the announced size
		if err := f.Truncate(m.Size); err != nil {
			f.Close()
			return nil, err
		}
		have = m.Size
	}
	return &entry{meta: m, f: f, have: have}, nil
}

// Reserve makes room for a share's upload and returns how many bytes
// of it the store already holds: the offset the daemon resumes from.
// A share already in the store keeps its bytes unless its size
// changed, which means a different file: it then starts over.
func (s *Store) Reserve(m Meta) (int64, error) {
	if !validID(m.ID) || m.Size < 1 {
		return 0, fmt.Errorf("upload: bad share %q", m.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[m.ID]; ok {
		if e.meta.Size == m.Size {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.have, nil
		}
		s.dropLocked(m.ID)
	}
	if s.budget <= 0 || s.reserved+m.Size > s.budget {
		metrics.UploadsRefused.Add(1)
		return 0, ErrFull
	}
	f, err := os.OpenFile(s.path(m.ID, ".data"), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	if err := s.writeMeta(m); err != nil {
		f.Close()
		s.remove(m.ID)
		return 0, err
	}
	s.entries[m.ID] = &entry{meta: m, f: f}
	s.reserved += m.Size
	return 0, nil
}

// writeMeta saves a share's metadata atomically: temp file, rename.
func (s *Store) writeMeta(m Meta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := s.path(m.ID, ".json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(m.ID, ".json"))
}

// Put appends data at offset and returns the bytes stored after it
// (the ack). Like layer C it only extends: data must start exactly
// where the stored bytes end and stay inside the reserved size, else
// nothing is written and the reply is the unchanged length.
func (s *Store) Put(id string, offset int64, data []byte) int64 {
	e := s.get(id)
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(data) == 0 || offset != e.have || e.have+int64(len(data)) > e.meta.Size {
		return e.have
	}
	n, err := e.f.WriteAt(data, offset)
	e.have += int64(n)
	metrics.UploadBytesStored.Add(int64(n))
	metrics.UploadBytesHeld.Add(int64(n))
	if err == nil && e.have == e.meta.Size {
		metrics.UploadsCompleted.Add(1)
	}
	return e.have
}

// Have returns how many bytes of a share the store holds, all of
// them from offset 0.
func (s *Store) Have(id string) int64 {
	e := s.get(id)
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.have
}

// Complete reports whether the whole file is stored.
func (s *Store) Complete(id string) bool {
	e := s.get(id)
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.have == e.meta.Size
}

// ReadAt fills p from a share's stored bytes at offset. It fails
// unless the whole of [offset, offset+len(p)) is stored.
func (s *Store) ReadAt(id string, p []byte, offset int64) error {
	e := s.get(id)
	if e == nil {
		return os.ErrNotExist
	}
	e.mu.Lock()
	have := e.have
	e.mu.Unlock()
	if offset < 0 || offset+int64(len(p)) > have {
		return errors.New("upload: range not stored")
	}
	_, err := e.f.ReadAt(p, offset)
	return err
}

// Stored lists every share in the store, for the relay to put back
// in its share table after a restart.
func (s *Store) Stored() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e.meta)
	}
	return out
}

// Drop deletes a share's bytes and metadata (expiry, kill).
func (s *Store) Drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropLocked(id)
}

func (s *Store) dropLocked(id string) {
	e, ok := s.entries[id]
	if !ok {
		return
	}
	delete(s.entries, id)
	s.reserved -= e.meta.Size
	e.mu.Lock()
	metrics.UploadBytesHeld.Add(-e.have)
	e.have = 0
	e.f.Close()
	e.mu.Unlock()
	s.remove(id)
}

// remove deletes a share's files, whatever state they're in.
func (s *Store) remove(id string) {
	for _, ext := range []string{".data", ".json", ".json.tmp"} {
		os.Remove(s.path(id, ext))
	}
}

// Reserved reports the bytes promised to uploads. For tests.
func (s *Store) Reserved() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved
}

func (s *Store) get(id string) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[id]
}

func (s *Store) held() int64 {
	var n int64
	for _, e := range s.entries {
		n += e.have
	}
	return n
}

func (s *Store) path(id, ext string) string { return filepath.Join(s.dir, id+ext) }

// validID accepts share ids as the protocol mints them (base64url),
// which also keeps every path inside the store's directory.
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
