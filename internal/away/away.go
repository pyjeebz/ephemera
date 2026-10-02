// Package away is layer C: the relay's volatile store of share
// prefix blobs, uploaded by the daemon after announcing (the
// "away mode" a sender opts into). Served only while the sender
// is offline; dropped with the share at expiry or kill. Never
// a whole file: the blob is a bounded prefix, sender-chosen.
package away

import (
	"sync"

	"ephemera.run/ephemera/internal/metrics"
)

// Store holds one appendable blob per share, under a byte budget.
type Store struct {
	mu     sync.Mutex
	budget int64
	blobs  map[string]*blob
}

type blob struct {
	data []byte
}

// New builds a store with a byte budget. budget <= 0 disables it:
// Put returns false, Read serves nothing.
func New(budget int64) *Store {
	return &Store{budget: budget, blobs: make(map[string]*blob)}
}

// Put appends data to a share's blob, extending it only if it
// reaches exactly offset+len(data) — the daemon streams in order.
// Refuses appends that would exceed the budget or overlap existing
// bytes. Returns the blob length after the append (the ack value).
func (s *Store) Put(shareID string, offset int64, data []byte) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.budget <= 0 || len(data) == 0 {
		return s.haveLocked(shareID)
	}
	b := s.blobs[shareID]
	if b == nil {
		b = &blob{}
		s.blobs[shareID] = b
	}
	if offset != int64(len(b.data)) {
		// out-of-order or duplicate: the daemon resumes from have()
		return int64(len(b.data))
	}
	if int64(len(b.data))+int64(len(data)) > s.budget {
		// budget exhausted for this blob: keep what we have
		return int64(len(b.data))
	}
	b.data = append(b.data, data...)
	metrics.AwayBytesStored.Add(int64(len(data)))
	metrics.AwayBytesHeld.Set(s.HeldLocked())
	return int64(len(b.data))
}

// Have returns the blob length for a share (the resume hint).
func (s *Store) Have(shareID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.haveLocked(shareID)
}

func (s *Store) haveLocked(shareID string) int64 {
	if b := s.blobs[shareID]; b != nil {
		return int64(len(b.data))
	}
	return 0
}

// Read returns the blob's bytes in [start, start+length), or nil
// when the range is not fully stored.
func (s *Store) Read(shareID string, start, length int64) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.blobs[shareID]
	if b == nil {
		return nil
	}
	if start < 0 || length < 0 || start+length > int64(len(b.data)) {
		return nil
	}
	return b.data[start : start+length]
}

// DropShare deletes a share's blob (expiry, kill).
func (s *Store) DropShare(shareID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blobs, shareID)
	metrics.AwayBytesHeld.Set(s.HeldLocked())
}

// HeldLocked reports total bytes stored; caller holds mu.
func (s *Store) HeldLocked() int64 {
	var n int64
	for _, b := range s.blobs {
		n += int64(len(b.data))
	}
	return n
}

// Held reports total bytes stored. For tests and metrics.
func (s *Store) Held() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.HeldLocked()
}
