// Package bulkhead gives every dependency its own *http.Transport and
// its own in-flight semaphore. With BULKHEAD=on a saturated
// non-critical pool cannot evict critical-path connections; with
// BULKHEAD=off all deps share one transport (one pool) and the topic's
// "shared fate via shared pool" failure shows up in the data.
package bulkhead

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// ErrPoolFull is returned by Pool.Acquire when LOAD_SHED=on and the
// in-flight semaphore is at capacity. The gateway converts this to a
// per-dep 503 in the inbound response.
var ErrPoolFull = errors.New("dep pool full (load shed)")

// Pool wraps an *http.Client + an atomic in-flight counter so the
// gateway can both call HTTP and report pool depth to Prometheus.
type Pool struct {
	Client   *http.Client
	Capacity int

	inflight int32
}

// Options for NewPool. Sensible defaults are chosen if a field is
// left zero so callers can stay terse.
type Options struct {
	// MaxConnsPerHost caps the transport's total connection count.
	MaxConnsPerHost int
	// MaxIdleConnsPerHost caps idle connections per host.
	MaxIdleConnsPerHost int
	// Timeout is the per-call deadline.
	Timeout time.Duration
}

// NewPool returns a Pool with its own transport so HOLs in this pool
// don't propagate. Capacity is set to MaxConnsPerHost.
func NewPool(opts Options) *Pool {
	if opts.MaxConnsPerHost <= 0 {
		opts.MaxConnsPerHost = 64
	}
	if opts.MaxIdleConnsPerHost <= 0 {
		opts.MaxIdleConnsPerHost = opts.MaxConnsPerHost
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 800 * time.Millisecond
	}

	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   500 * time.Millisecond,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxConnsPerHost:     opts.MaxConnsPerHost,
		MaxIdleConnsPerHost: opts.MaxIdleConnsPerHost,
		IdleConnTimeout:     60 * time.Second,
		ForceAttemptHTTP2:   false,
	}
	return &Pool{
		Client:   &http.Client{Transport: tr, Timeout: opts.Timeout},
		Capacity: opts.MaxConnsPerHost,
	}
}

// Acquire increments the in-flight counter. When `loadShed` is true
// and the pool is already at capacity, returns ErrPoolFull without
// holding a slot. The shed decision is per-call so the gateway can
// flip LOAD_SHED on/off at runtime via /admin/config.
//
// The returned release MUST be called when the call completes.
func (p *Pool) Acquire(_ context.Context, loadShed bool) (release func(), err error) {
	cur := atomic.AddInt32(&p.inflight, 1)
	if loadShed && int(cur) > p.Capacity {
		atomic.AddInt32(&p.inflight, -1)
		return func() {}, ErrPoolFull
	}
	return func() { atomic.AddInt32(&p.inflight, -1) }, nil
}

// Inflight returns the current pool depth (cheap to call from the
// metric exporter goroutine).
func (p *Pool) Inflight() int { return int(atomic.LoadInt32(&p.inflight)) }
