// Package cache is the tiny in-memory object store behind the lab-6-2
// caching proxy. It deliberately keeps EXPIRED entries around (it never
// evicts on expiry) so the proxy can serve them as stale-if-error
// last-known-good content during an origin outage. Entries leave the
// store only when overwritten or explicitly purged.
package cache

import (
	"net/http"
	"sync"
	"time"
)

// Entry is one cached upstream response.
type Entry struct {
	// Path is the request path the entry was stored under (without the
	// query string) so PurgePath can match every fragmented variant of
	// the same object at once.
	Path     string
	Status   int
	Header   http.Header
	Body     []byte
	StoredAt time.Time
	TTL      time.Duration
}

// Fresh reports whether the entry is still within its TTL.
func (e *Entry) Fresh(now time.Time) bool {
	return now.Before(e.StoredAt.Add(e.TTL))
}

// Store is a concurrency-safe map of cache key -> Entry.
type Store struct {
	mu sync.RWMutex
	m  map[string]*Entry
}

func New() *Store {
	return &Store{m: make(map[string]*Entry)}
}

// Get returns the entry for key (fresh or stale) and whether it exists.
func (s *Store) Get(key string) (*Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.m[key]
	return e, ok
}

// Set stores (or overwrites) the entry for key.
func (s *Store) Set(key string, e *Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = e
}

// Len is the live cache-entry cardinality.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.m)
}

// PurgePath deletes every entry whose Path equals path (i.e. all
// fragmented query-string variants of one object). Returns the count
// removed - this is exactly the "expire a popular object across the
// edge" primitive the thundering-herd step uses.
func (s *Store) PurgePath(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, e := range s.m {
		if e.Path == path {
			delete(s.m, k)
			n++
		}
	}
	return n
}

// Flush removes everything. Returns the count removed.
func (s *Store) Flush() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.m)
	s.m = make(map[string]*Entry)
	return n
}
