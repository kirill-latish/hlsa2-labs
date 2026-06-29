// Package proxy holds the backend pool, per-backend health state, and
// the two balancing algorithms (round-robin, least-conn) used by the
// edge-proxy. Keeping it here keeps cmd/edge-proxy/main.go readable.
package proxy

import (
	"sync"
	"sync/atomic"
)

// Backend is one upstream instance the edge routes to.
type Backend struct {
	ID  string
	URL string // base URL, e.g. http://backend-1:8081

	mu               sync.Mutex
	healthy          bool
	consecutiveFails int
	consecutiveOK    int

	inflight int64 // atomic
	requests int64 // atomic - total routed (lifetime)
}

// Healthy reports whether the proxy currently considers this backend
// routable.
func (b *Backend) Healthy() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.healthy
}

// ConsecutiveFails returns the current run of failed health checks.
func (b *Backend) ConsecutiveFails() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.consecutiveFails
}

// MarkProbe folds one health-probe result into the backend state using
// the failure threshold. A single success restores a down backend
// (fast recovery); `threshold` consecutive failures take a healthy one
// down. Returns the new healthy state.
func (b *Backend) MarkProbe(ok bool, threshold int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.consecutiveOK++
		b.consecutiveFails = 0
		if !b.healthy && b.consecutiveOK >= 1 {
			b.healthy = true
		}
	} else {
		b.consecutiveFails++
		b.consecutiveOK = 0
		if b.healthy && b.consecutiveFails >= threshold {
			b.healthy = false
		}
	}
	return b.healthy
}

// SetHealthy force-sets the state (used at startup).
func (b *Backend) SetHealthy(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.healthy = v
}

func (b *Backend) Inflight() int64  { return atomic.LoadInt64(&b.inflight) }
func (b *Backend) Requests() int64  { return atomic.LoadInt64(&b.requests) }
func (b *Backend) Acquire()         { atomic.AddInt64(&b.inflight, 1) }
func (b *Backend) Release()         { atomic.AddInt64(&b.inflight, -1) }
func (b *Backend) CountRequest()    { atomic.AddInt64(&b.requests, 1) }

// Pool is the ordered set of backends plus a round-robin cursor.
type Pool struct {
	backends []*Backend
	rr       uint64 // atomic
}

func NewPool(backends []*Backend) *Pool {
	return &Pool{backends: backends}
}

// Backends returns the full ordered list (for health loops and status).
func (p *Pool) Backends() []*Backend { return p.backends }

// ByID returns the backend with the given id, or nil.
func (p *Pool) ByID(id string) *Backend {
	for _, b := range p.backends {
		if b.ID == id {
			return b
		}
	}
	return nil
}

// Pick selects a healthy backend by algorithm. Returns nil when no
// healthy backend exists (the edge then emits 503).
//
//	round-robin -> even by request count, ignores load
//	least-conn  -> fewest in-flight requests, rebalances uneven cost
func (p *Pool) Pick(algo string) *Backend {
	healthy := make([]*Backend, 0, len(p.backends))
	for _, b := range p.backends {
		if b.Healthy() {
			healthy = append(healthy, b)
		}
	}
	if len(healthy) == 0 {
		return nil
	}
	switch algo {
	case "least-conn":
		best := healthy[0]
		for _, b := range healthy[1:] {
			if b.Inflight() < best.Inflight() {
				best = b
			}
		}
		return best
	default: // round-robin
		n := atomic.AddUint64(&p.rr, 1)
		return healthy[(n-1)%uint64(len(healthy))]
	}
}
