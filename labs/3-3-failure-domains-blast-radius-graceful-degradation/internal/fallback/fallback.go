// Package fallback gives the gateway a per-dep last-known-good cache
// it can serve from when FALLBACK=on and a non-critical dep fails.
// The cache lives in the same process as the gateway so a dep being
// down does NOT prevent serving the cached value (different failure
// domain than the dep).
//
// Critical deps don't use this package - the topic teaches that the
// only correct response to a critical-dep outage is to fail the
// request, not to lie about it.
package fallback

import (
	"sync"
	"time"
)

// Entry is one cached response from a dep.
type Entry struct {
	Payload   []byte
	StoredAt  time.Time
	SourceDep string
}

// Cache is a per-process map of dep -> last successful payload. Hits
// after `maxAge` are still served (with `Stale=true`) so a degraded
// response is still better than no response for non-critical deps.
type Cache struct {
	mu     sync.RWMutex
	maxAge time.Duration
	store  map[string]Entry
}

// NewCache returns a cache. maxAge is informational only.
func NewCache(maxAge time.Duration) *Cache {
	return &Cache{
		maxAge: maxAge,
		store:  make(map[string]Entry, 8),
	}
}

// Put writes the latest good response for a dep.
func (c *Cache) Put(dep string, payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	c.store[dep] = Entry{Payload: cp, StoredAt: time.Now(), SourceDep: dep}
}

// Get returns the LKG entry for a dep, or ok=false if none exists.
// stale=true if StoredAt+maxAge < now, but the entry is still
// returned (callers can decide to use it or omit).
func (c *Cache) Get(dep string) (e Entry, stale, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	got, exists := c.store[dep]
	if !exists {
		return Entry{}, false, false
	}
	stale = time.Since(got.StoredAt) > c.maxAge
	return got, stale, true
}

// Has reports whether there is any LKG entry for the dep, without
// copying the payload.
func (c *Cache) Has(dep string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.store[dep]
	return ok
}
