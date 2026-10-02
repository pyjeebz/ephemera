// Package cache is layer B: a volatile store of already-served
// ranges, keyed by share id and chunk offset, bounded by a byte
// budget and dropped wholesale when a share expires. It only ever
// holds bytes that already crossed the relay on their way to some
// receiver — no fetch-ahead, no upload.
package cache

import (
	"container/list"
	"sync"

	"ephemera.run/ephemera/internal/metrics"
)

// Store is a bounded LRU of chunks.
type Store struct {
	mu      sync.Mutex
	budget  int64
	held    int64
	entries map[chunkKey]*list.Element
	order   *list.List // front = freshest
}

// chunkKey identifies one cached byte range of one share.
type chunkKey struct {
	shareID string
	offset  int64
	length  int64
}

type chunk struct {
	key  chunkKey
	data []byte
}

// New builds a store with a byte budget. budget <= 0 disables it:
// Put becomes a no-op, Get always misses.
func New(budget int64) *Store {
	return &Store{
		budget:  budget,
		entries: make(map[chunkKey]*list.Element),
		order:   list.New(),
	}
}

// Put stores a chunk, evicting least-recently-used entries until
// the budget holds it. Chunks larger than the whole budget are not
// stored.
func (s *Store) Put(shareID string, offset int64, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.budget <= 0 || len(data) == 0 {
		return
	}
	key := chunkKey{shareID: shareID, offset: offset, length: int64(len(data))}
	if el, ok := s.entries[key]; ok {
		s.order.MoveToFront(el)
		return // same-length chunk: content identical, keep old
	}
	if int64(len(data)) > s.budget {
		return
	}
	for s.held+int64(len(data)) > s.budget && s.order.Len() > 0 {
		s.evictOldest()
	}
	c := chunk{key: key, data: append([]byte(nil), data...)}
	s.entries[key] = s.order.PushFront(&c)
	s.held += int64(len(data))
	metrics.CacheBytesHeld.Set(s.held)
}

// Get returns the chunk covering [offset, offset+length) of a
// share, or nil. Exact-match only: the relay stores and asks in
// the same chunk granularity, so partial hits would mean a
// granularity bug elsewhere.
func (s *Store) Get(shareID string, offset, length int64) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := chunkKey{shareID: shareID, offset: offset, length: length}
	el, ok := s.entries[key]
	if !ok {
		return nil
	}
	s.order.MoveToFront(el)
	c := el.Value.(*chunk)
	return c.data
}

// DropShare evicts everything belonging to one share: called when
// the share expires, dies, or is killed.
func (s *Store) DropShare(shareID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for el := s.order.Back(); el != nil; {
		prev := el.Prev()
		c := el.Value.(*chunk)
		if c.key.shareID == shareID {
			s.order.Remove(el)
			delete(s.entries, c.key)
			s.held -= c.key.length
		}
		el = prev
	}
	metrics.CacheBytesHeld.Set(s.held)
}

// Held reports current byte occupancy. For tests and metrics.
func (s *Store) Held() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

func (s *Store) evictOldest() {
	el := s.order.Back()
	if el == nil {
		return
	}
	c := el.Value.(*chunk)
	s.order.Remove(el)
	delete(s.entries, c.key)
	s.held -= c.key.length
}
