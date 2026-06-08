// Package shardkey decides which collection an event lands in and what
// shape the document takes.
//
// Four strategies, all driven by the same input (tenant_id, user_id):
//
//	candidate     - {tenant_id} only          - the deliberately-bad starting point
//	hash-suffix   - {tenant_partition}        - a hot tenant fans out into N buckets
//	composite-key - {tenant_id, user_hash}    - balances within a tenant
//	resharded     - {user_hash} (hashed)      - a fresh shard key
package shardkey

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/hlsa2-labs/lab4-3/internal/payloads"
)

type Strategy string

const (
	Candidate    Strategy = "candidate"
	HashSuffix   Strategy = "hash-suffix"
	CompositeKey Strategy = "composite-key"
	Resharded    Strategy = "resharded"
)

// CollectionFor returns the lab43.<collection> name for the given strategy.
func CollectionFor(s Strategy) (string, error) {
	switch s {
	case Candidate:
		return "events_candidate", nil
	case HashSuffix:
		return "events_hash_suffix", nil
	case CompositeKey:
		return "events_composite", nil
	case Resharded:
		return "events_resharded", nil
	default:
		return "", fmt.Errorf("unknown shard key strategy %q", s)
	}
}

// Parse parses a strategy name. Accepts both "candidate" and "fixed" - the
// latter is what `make bench-skew SHARD_KEY=fixed` resolves to. Callers
// who want the fixed strategy must pass `fixedFallback`, the strategy
// recorded by `make apply-fix CANDIDATE=...`.
func Parse(name, fixedFallback string) (Strategy, error) {
	if name == "fixed" {
		if fixedFallback == "" {
			return "", fmt.Errorf("SHARD_KEY=fixed but no candidate has been applied (run `make apply-fix CANDIDATE=...`)")
		}
		name = fixedFallback
	}
	switch Strategy(name) {
	case Candidate, HashSuffix, CompositeKey, Resharded:
		return Strategy(name), nil
	default:
		return "", fmt.Errorf("unknown shard key strategy %q", name)
	}
}

// Builder describes the fields the loadgen needs to add to each event so
// that the collection's shard key resolves cleanly. Two of the
// strategies derive their key fields from the user_id and an optional
// hash-suffix bucket count.
type Builder struct {
	Strategy          Strategy
	HashSuffixBuckets int
	HotTenantID       string
}

// NewBuilder constructs a key builder. `buckets` must be > 0 when using
// HashSuffix.
func NewBuilder(s Strategy, buckets int, hotTenant string) Builder {
	if buckets <= 0 {
		buckets = 16
	}
	return Builder{Strategy: s, HashSuffixBuckets: buckets, HotTenantID: strings.TrimSpace(hotTenant)}
}

// Apply mutates the event in place so the fields required by the
// strategy's shard key are populated. Always populates UserHash for the
// composite/resharded strategies; populates TenantPartition for
// hash-suffix.
func (b Builder) Apply(e *payloads.MongoEvent, counter int64) {
	uh := userHash(e.UserID)
	e.UserHash = uh

	switch b.Strategy {
	case HashSuffix:
		bucket := counter % int64(b.HashSuffixBuckets)
		// All tenants get a partition - if we only suffixed the hot
		// tenant, mongos couldn't compile a single targeted query plan.
		e.TenantPartition = fmt.Sprintf("%s:%02d", e.TenantID, bucket)
	case CompositeKey:
		// composite key {tenant_id, user_hash} is already populated.
	case Resharded:
		// resharded is just user_hash hashed.
	case Candidate:
		// nothing extra.
	}
}

// userHash is a stable 32-bit hash over user_id used by the
// composite/resharded keys. Values are clamped into [0, 1024) to keep
// the chunk count bounded.
func userHash(userID int64) int32 {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "u:%d", userID)
	return int32(h.Sum32() % 1024)
}
