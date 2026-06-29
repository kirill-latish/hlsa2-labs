// Package lru is a tiny, dependency-free in-process LRU map with a
// per-entry TTL. It is the "local-LRU" mitigation the topic guide
// applies to short-circuit a hot key: the hottest keys are served
// from app memory at near-zero network cost, so they barely touch the
// shared (sharded) Redis at all.
//
// The trade-off the guide asks students to name lives here too: each
// app process keeps its own copy, so for the freshness window of the
// local TTL different app instances can disagree about the hot key's
// value. We intentionally keep that window short (seconds).
package lru

import (
	"container/list"
	"sync"
	"time"
)

type entry struct {
	key       string
	value     string
	version   int64
	expiresAt time.Time
}

// Cache is a fixed-capacity LRU with TTL eviction. Safe for concurrent
// use. Capacity <= 0 disables the cache (every Get is a miss).
type Cache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	ll       *list.List
	items    map[string]*list.Element

	// evictions counts capacity-driven evictions (not TTL expiries) so
	// the caller can export an eviction-rate metric.
	evictions int64
}

// New returns an LRU with the given capacity and per-entry TTL.
func New(capacity int, ttl time.Duration) *Cache {
	return &Cache{
		capacity: capacity,
		ttl:      ttl,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
	}
}

// Reconfigure resizes the cache and changes the TTL at runtime. If the
// new capacity is smaller, the coldest entries are evicted.
func (c *Cache) Reconfigure(capacity int, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.capacity = capacity
	c.ttl = ttl
	c.trimLocked()
}

// Get returns the value if present and unexpired.
func (c *Cache) Get(key string) (value string, version int64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.capacity <= 0 {
		return "", 0, false
	}
	el, found := c.items[key]
	if !found {
		return "", 0, false
	}
	en := el.Value.(*entry)
	if time.Now().After(en.expiresAt) {
		c.removeLocked(el)
		return "", 0, false
	}
	c.ll.MoveToFront(el)
	return en.value, en.version, true
}

// Add inserts or refreshes a key. Returns the number of evictions
// performed by this call (0 or 1) so the caller can count them.
func (c *Cache) Add(key, value string, version int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.capacity <= 0 {
		return 0
	}
	if el, found := c.items[key]; found {
		en := el.Value.(*entry)
		en.value = value
		en.version = version
		en.expiresAt = time.Now().Add(c.ttl)
		c.ll.MoveToFront(el)
		return 0
	}
	en := &entry{key: key, value: value, version: version, expiresAt: time.Now().Add(c.ttl)}
	el := c.ll.PushFront(en)
	c.items[key] = el
	if c.ll.Len() > c.capacity {
		c.evictOldestLocked()
		return 1
	}
	return 0
}

// Delete removes a key (used by explicit invalidation).
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, found := c.items[key]; found {
		c.removeLocked(el)
	}
}

// Purge empties the cache.
func (c *Cache) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = make(map[string]*list.Element)
}

// Len reports the current occupancy.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Evictions reports the cumulative capacity-driven eviction count.
func (c *Cache) Evictions() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evictions
}

func (c *Cache) trimLocked() {
	for c.capacity > 0 && c.ll.Len() > c.capacity {
		c.evictOldestLocked()
	}
}

func (c *Cache) evictOldestLocked() {
	el := c.ll.Back()
	if el != nil {
		c.removeLocked(el)
		c.evictions++
	}
}

func (c *Cache) removeLocked(el *list.Element) {
	c.ll.Remove(el)
	delete(c.items, el.Value.(*entry).key)
}
