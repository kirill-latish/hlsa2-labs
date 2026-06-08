// Package freshness implements the three read policies the polyglot
// bench picks between (read-from-sor, read-from-derived, lsn-wait).
//
// The bench's request flow is roughly:
//
//	w_lsn := WritePostgres(...)
//	val   := Read(ctx, w_lsn)
//
// Each policy has its own definition of what "current enough" means
// when reading from the derived store (Elasticsearch).
package freshness

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Policy string

const (
	ReadFromSoR     Policy = "read-from-sor"
	ReadFromDerived Policy = "read-from-derived"
	LSNWait         Policy = "lsn-wait"
)

// Parse parses a policy name with a friendly error.
func Parse(name string) (Policy, error) {
	p := Policy(strings.TrimSpace(name))
	switch p {
	case ReadFromSoR, ReadFromDerived, LSNWait:
		return p, nil
	}
	return "", fmt.Errorf("unknown freshness policy %q", name)
}

// SoRReader reads the authoritative value from Postgres given a primary
// key. Used by ReadFromSoR (and as the fallback path for LSNWait).
type SoRReader interface {
	ReadFromSoR(ctx context.Context, productID int64) (val string, lsn string, err error)
}

// DerivedReader reads from Elasticsearch.
type DerivedReader interface {
	ReadFromDerived(ctx context.Context, productID int64) (val string, lsn string, err error)
}

// LagSampler returns the latest LSN the es-consumer has indexed up to.
// Implementations cache this for ~100ms.
type LagSampler interface {
	IndexedLSN(ctx context.Context) (string, error)
}

// Decision is the outcome of a single read.
type Decision struct {
	Source        string        // "sor" | "derived" | "fallback-sor"
	WaitedForLSN  string        // empty unless LSNWait waited
	Waited        time.Duration // 0 unless LSNWait waited
	Stale         bool          // for ReadFromDerived
	Value         string
}

// Reader is the public entry point.
type Reader struct {
	SoR     SoRReader
	Derived DerivedReader
	Lag     LagSampler

	// LSNWaitMax bounds how long LSNWait will block before falling back
	// to SoR.
	LSNWaitMax time.Duration

	// PollInterval is how often LSNWait re-checks the indexed LSN.
	PollInterval time.Duration
}

// Read executes the requested policy.
func (r Reader) Read(ctx context.Context, p Policy, productID int64, writeLSN string) (Decision, error) {
	switch p {
	case ReadFromSoR:
		return r.readSoR(ctx, productID)
	case ReadFromDerived:
		return r.readDerived(ctx, productID, writeLSN)
	case LSNWait:
		return r.readLSNWait(ctx, productID, writeLSN)
	}
	return Decision{}, fmt.Errorf("unknown policy %q", p)
}

func (r Reader) readSoR(ctx context.Context, productID int64) (Decision, error) {
	val, lsn, err := r.SoR.ReadFromSoR(ctx, productID)
	if err != nil {
		return Decision{}, err
	}
	return Decision{Source: "sor", Value: val, WaitedForLSN: lsn}, nil
}

func (r Reader) readDerived(ctx context.Context, productID int64, writeLSN string) (Decision, error) {
	val, lsn, err := r.Derived.ReadFromDerived(ctx, productID)
	if err != nil {
		return Decision{}, err
	}
	stale := writeLSN != "" && lsnLess(lsn, writeLSN)
	return Decision{Source: "derived", Value: val, WaitedForLSN: lsn, Stale: stale}, nil
}

func (r Reader) readLSNWait(ctx context.Context, productID int64, writeLSN string) (Decision, error) {
	if writeLSN == "" {
		// Caller did not capture a write LSN; behave like ReadFromDerived.
		return r.readDerived(ctx, productID, "")
	}
	deadline := time.Now().Add(r.LSNWaitMax)
	poll := r.PollInterval
	if poll <= 0 {
		poll = 25 * time.Millisecond
	}
	waitedStart := time.Now()
	for {
		curr, err := r.Lag.IndexedLSN(ctx)
		if err == nil && !lsnLess(curr, writeLSN) {
			val, lsn, err := r.Derived.ReadFromDerived(ctx, productID)
			if err == nil {
				return Decision{Source: "derived", Value: val, WaitedForLSN: lsn, Waited: time.Since(waitedStart)}, nil
			}
		}
		if time.Now().After(deadline) {
			val, lsn, err := r.SoR.ReadFromSoR(ctx, productID)
			if err != nil {
				return Decision{}, err
			}
			return Decision{Source: "fallback-sor", Value: val, WaitedForLSN: lsn, Waited: time.Since(waitedStart)}, nil
		}
		select {
		case <-ctx.Done():
			return Decision{}, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// lsnLess reports a < b for "0/AABBCCDD"-style LSNs. Returns false when
// either side is empty (the caller treats that as "no LSN to compare").
func lsnLess(a, b string) bool {
	an, aok := parseLSN(a)
	bn, bok := parseLSN(b)
	if !aok || !bok {
		return false
	}
	return an < bn
}

// parseLSN converts "AAAAAAAA/BBBBBBBB" or a plain decimal/hex string
// into an int64 byte position.
func parseLSN(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		hi, err1 := strconv.ParseInt(s[:i], 16, 64)
		lo, err2 := strconv.ParseInt(s[i+1:], 16, 64)
		if err1 != nil || err2 != nil {
			return 0, false
		}
		return hi<<32 | lo, true
	}
	// plain integer
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v, true
	}
	if v, err := strconv.ParseInt(s, 16, 64); err == nil {
		return v, true
	}
	return 0, false
}
