// Package shard implements CLIENT-SIDE sharding across N standalone
// Redis nodes.
//
// DECISION (documented in the README): instead of running Redis Cluster
// with gossip/slot migration, the app hashes each key with CRC32 and
// maps it to one of the standalone nodes (hash % N). This reproduces
// exactly the property the topic guide needs - a single hot key
// deterministically lands on ONE node, so per-shard imbalance is
// visible - while being far more robust in a laptop Docker stack
// (no cluster bus, no slot rebalancing, no MOVED redirects).
package shard

import (
	"hash/crc32"

	"github.com/redis/go-redis/v9"
)

// Ring holds the ordered set of standalone Redis clients and their
// human-readable node names (redis-1, redis-2, ...).
type Ring struct {
	clients []*redis.Client
	names   []string
}

// New builds a ring from parallel slices of node names and clients.
func New(names []string, clients []*redis.Client) *Ring {
	return &Ring{clients: clients, names: names}
}

// Index returns the shard index a key maps to (CRC32 % N). Exposed so
// callers/tests can reason about placement deterministically.
func (r *Ring) Index(key string) int {
	if len(r.clients) == 0 {
		return 0
	}
	return int(crc32.ChecksumIEEE([]byte(key))) % len(r.clients)
}

// For returns the Redis client and node name responsible for a key.
func (r *Ring) For(key string) (*redis.Client, string) {
	i := r.Index(key)
	return r.clients[i], r.names[i]
}

// Names returns the node names in ring order.
func (r *Ring) Names() []string { return r.names }

// Clients returns the clients in ring order.
func (r *Ring) Clients() []*redis.Client { return r.clients }

// Len returns the number of shards.
func (r *Ring) Len() int { return len(r.clients) }
