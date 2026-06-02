// Package readpolicy implements the four read-after-write modes
// referenced by topic 248 step 4. The raw-bench harness instantiates
// one of these per worker and asks it to Read after each Write.
//
//	naive        - random replica, no coordination
//	session-pin  - reads from primary for TTL after this session wrote
//	lsn-wait     - capture writer LSN, ask each replica's
//	               pg_last_wal_replay_lsn(), pick the first that's caught
//	               up; brief wait then primary fallback
//	primary-read - global TTL: any read of a recently-written entity
//	               goes to primary
package readpolicy

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/hlsa2-labs/lab4-2/internal/lsn"
)

// Mode names match the topic guide and Make targets.
const (
	ModeNaive       = "naive"
	ModeSessionPin  = "session-pin"
	ModeLSNWait     = "lsn-wait"
	ModePrimaryRead = "primary-read"
)

// Endpoint is one Postgres connection pool labelled with a name. The
// raw-bench passes in [primary, replica1, replica2].
type Endpoint struct {
	Name string
	Pool *pgxpool.Pool
}

// Config tunes the policy.
type Config struct {
	SessionPinTTL  time.Duration
	PrimaryReadTTL time.Duration
	LSNWaitMax     time.Duration
	LSNPollEvery   time.Duration
}

// DefaultConfig is what raw-bench uses if nothing else is set.
func DefaultConfig() Config {
	return Config{
		SessionPinTTL:  1500 * time.Millisecond,
		PrimaryReadTTL: 1500 * time.Millisecond,
		LSNWaitMax:     800 * time.Millisecond,
		LSNPollEvery:   5 * time.Millisecond,
	}
}

// Picker chooses an endpoint to read from. Concrete implementations
// hold whatever state the policy needs (last write time, last LSN, ...)
type Picker interface {
	// RecordWrite is called by the raw-bench *after* the write commits.
	RecordWrite(ctx context.Context, sessionID, entityKey string, writeLSN lsn.LSN, at time.Time)
	// Pick returns the endpoint that should serve the read for this
	// session/entity. It may also return how long it waited (LSN-wait).
	Pick(ctx context.Context, sessionID, entityKey string) (Endpoint, time.Duration, error)
}

// New returns a Picker for the requested mode.
func New(mode string, primary Endpoint, replicas []Endpoint, cfg Config) (Picker, error) {
	if primary.Pool == nil {
		return nil, errors.New("readpolicy: primary endpoint required")
	}
	if len(replicas) == 0 && mode != ModePrimaryRead {
		return nil, errors.New("readpolicy: at least one replica required")
	}
	switch mode {
	case ModeNaive:
		return &naive{replicas: replicas, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}, nil
	case ModeSessionPin:
		return &sessionPin{
			primary:   primary,
			replicas:  replicas,
			ttl:       cfg.SessionPinTTL,
			lastWrite: make(map[string]time.Time),
			rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
		}, nil
	case ModeLSNWait:
		return &lsnWait{
			primary:   primary,
			replicas:  replicas,
			maxWait:   cfg.LSNWaitMax,
			pollEvery: cfg.LSNPollEvery,
			rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
		}, nil
	case ModePrimaryRead:
		return &primaryRead{
			primary:    primary,
			replicas:   replicas,
			ttl:        cfg.PrimaryReadTTL,
			lastEntity: make(map[string]time.Time),
			rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
		}, nil
	default:
		return nil, fmt.Errorf("readpolicy: unknown mode %q", mode)
	}
}

// ----- naive -----

type naive struct {
	replicas []Endpoint
	rng      *rand.Rand
	mu       sync.Mutex
}

func (n *naive) RecordWrite(_ context.Context, _, _ string, _ lsn.LSN, _ time.Time) {}

func (n *naive) Pick(_ context.Context, _, _ string) (Endpoint, time.Duration, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	idx := n.rng.Intn(len(n.replicas))
	return n.replicas[idx], 0, nil
}

// ----- session-pin -----

type sessionPin struct {
	primary  Endpoint
	replicas []Endpoint
	ttl      time.Duration

	mu        sync.Mutex
	lastWrite map[string]time.Time
	rng       *rand.Rand
}

func (s *sessionPin) RecordWrite(_ context.Context, sessionID, _ string, _ lsn.LSN, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastWrite[sessionID] = at
}

func (s *sessionPin) Pick(_ context.Context, sessionID, _ string) (Endpoint, time.Duration, error) {
	s.mu.Lock()
	last, hot := s.lastWrite[sessionID]
	hot = hot && time.Since(last) < s.ttl
	if hot {
		s.mu.Unlock()
		return s.primary, 0, nil
	}
	idx := s.rng.Intn(len(s.replicas))
	s.mu.Unlock()
	return s.replicas[idx], 0, nil
}

// ----- lsn-wait -----

type lsnWait struct {
	primary   Endpoint
	replicas  []Endpoint
	maxWait   time.Duration
	pollEvery time.Duration

	mu          sync.Mutex
	lastWrites  map[string]lsn.LSN // entityKey -> writer LSN
	rng         *rand.Rand
}

func (l *lsnWait) RecordWrite(_ context.Context, _, entityKey string, writeLSN lsn.LSN, _ time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastWrites == nil {
		l.lastWrites = make(map[string]lsn.LSN)
	}
	l.lastWrites[entityKey] = writeLSN
}

func (l *lsnWait) Pick(ctx context.Context, _, entityKey string) (Endpoint, time.Duration, error) {
	l.mu.Lock()
	target, ok := l.lastWrites[entityKey]
	l.mu.Unlock()
	if !ok || target == 0 {
		// No recorded write - any replica is fine.
		idx := 0
		if len(l.replicas) > 0 {
			l.mu.Lock()
			idx = l.rng.Intn(len(l.replicas))
			l.mu.Unlock()
		}
		return l.replicas[idx], 0, nil
	}

	// Try replicas in random order so we don't pin the same one every
	// time after a write storm.
	l.mu.Lock()
	order := l.rng.Perm(len(l.replicas))
	l.mu.Unlock()

	deadline := time.Now().Add(l.maxWait)
	var totalWait time.Duration
	for _, idx := range order {
		ep := l.replicas[idx]
		waitCtx, cancel := context.WithDeadline(ctx, deadline)
		waited, err := lsn.WaitForReplay(waitCtx, ep.Pool, target, time.Until(deadline), l.pollEvery)
		cancel()
		totalWait += waited
		if err == nil {
			return ep, totalWait, nil
		}
	}

	// All replicas timed out. Fall back to primary.
	return l.primary, totalWait, nil
}

// ----- primary-read -----

type primaryRead struct {
	primary  Endpoint
	replicas []Endpoint
	ttl      time.Duration

	mu         sync.Mutex
	lastEntity map[string]time.Time
	rng        *rand.Rand
}

func (p *primaryRead) RecordWrite(_ context.Context, _, entityKey string, _ lsn.LSN, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastEntity[entityKey] = at
}

func (p *primaryRead) Pick(_ context.Context, _, entityKey string) (Endpoint, time.Duration, error) {
	p.mu.Lock()
	last, hot := p.lastEntity[entityKey]
	hot = hot && time.Since(last) < p.ttl
	if hot {
		p.mu.Unlock()
		return p.primary, 0, nil
	}
	if len(p.replicas) == 0 {
		p.mu.Unlock()
		return p.primary, 0, nil
	}
	idx := p.rng.Intn(len(p.replicas))
	p.mu.Unlock()
	return p.replicas[idx], 0, nil
}
