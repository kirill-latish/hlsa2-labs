// Package retry implements a global token-bucket retry budget plus
// exponential backoff with full jitter. The topic uses the term
// "retry budget" specifically - it is a counter that, if exhausted,
// denies the retry rather than the attempt: that distinction matters
// for storm prevention so we surface both outcomes as metrics.
package retry

import (
	"math/rand"
	"sync"
	"time"
)

// Budget is a global token bucket. Tokens accrue at rate `refillPerSec`
// and burst is capped at `burst`. Each retry attempt costs 1 token;
// when the bucket is empty, retries are denied.
type Budget struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	rate   float64
	last   time.Time
}

// NewBudget builds a budget. A common choice is refill = 10% of
// inbound RPS, burst = 1s of refill.
func NewBudget(refillPerSec, burst float64) *Budget {
	if refillPerSec < 0 {
		refillPerSec = 0
	}
	if burst < 1 {
		burst = 1
	}
	return &Budget{
		tokens: burst,
		burst:  burst,
		rate:   refillPerSec,
		last:   time.Now(),
	}
}

// TryConsume returns true if a token was available (and consumed).
func (b *Budget) TryConsume() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += elapsed * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Snapshot returns tokens remaining (mostly for tests/metrics).
func (b *Budget) Snapshot() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens
}

// Backoff returns the wait before retry `attempt` (zero-indexed) with
// full jitter capped at `max`.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	exp := base * (1 << uint(attempt))
	if exp > max || exp <= 0 {
		exp = max
	}
	if exp <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(exp)))
}
