// Package breaker implements a tiny tripped-on-failures circuit
// breaker. It is intentionally minimal - the topic makes the point
// that a breaker reduces blast radius but does not remove temporal
// coupling, and we want students to see that in the numbers, not get
// distracted by a full hystrix-style implementation.
//
// Ported from lab 3-2 with the same surface area (Do, Snapshot) so
// porting more behaviour back to lab 3-2 stays straightforward.
package breaker

import (
	"errors"
	"sync"
	"time"
)

type State int

const (
	Closed State = iota
	HalfOpen
	Open
)

// ErrOpen is returned from Do when the breaker is open.
var ErrOpen = errors.New("circuit breaker open")

// Breaker is a single closed/halfopen/open circuit.
type Breaker struct {
	mu sync.Mutex

	state     State
	failures  int
	threshold int
	cooldown  time.Duration
	openedAt  time.Time
}

// New returns a breaker that trips after `failureThreshold` consecutive
// failures and stays open for `cooldown` before allowing a probe.
func New(failureThreshold int, cooldown time.Duration) *Breaker {
	if failureThreshold <= 0 {
		failureThreshold = 5
	}
	if cooldown <= 0 {
		cooldown = 2 * time.Second
	}
	return &Breaker{
		threshold: failureThreshold,
		cooldown:  cooldown,
	}
}

// Do runs fn while honouring the breaker state. Returns ErrOpen
// immediately when open.
func (b *Breaker) Do(fn func() error) error {
	b.mu.Lock()
	if b.state == Open {
		if time.Since(b.openedAt) > b.cooldown {
			b.state = HalfOpen
		} else {
			b.mu.Unlock()
			return ErrOpen
		}
	}
	b.mu.Unlock()

	err := fn()

	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.failures++
		if b.state == HalfOpen || b.failures >= b.threshold {
			b.state = Open
			b.openedAt = time.Now()
		}
		return err
	}
	b.failures = 0
	b.state = Closed
	return nil
}

// Snapshot returns a copy of the state for metrics export.
func (b *Breaker) Snapshot() (state State, failures int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.failures
}
