// Package saga is the orchestrator-side state machine for the
// place-order saga: payment.charge -> inventory.reserve ->
// shipping.schedule. Each forward step has a paired compensation
// (refund, release-stock, cancel-shipping). The orchestrator runs
// forward; on failure it walks the completed-steps stack in reverse
// and fires compensations.
package saga

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Step is one hop in the saga. Forward applies the change. Compensate
// undoes it. Both must be idempotent because retries are expected.
type Step struct {
	Name       string
	Forward    func(ctx context.Context) error
	Compensate func(ctx context.Context) error
}

// Result reports what happened during execution. Used to write
// per-request rows into the bench summary.
type Result struct {
	OK         bool
	FailedAt   string        // step name that failed; empty on success
	Compensated bool         // did all compensations run cleanly
	Latency    time.Duration
}

// Run executes the saga. retryEach is how many times each forward
// step may be retried before giving up. compensationRetries applies
// the same budget to compensations.
func Run(ctx context.Context, steps []Step, retryEach, compensationRetries int) Result {
	start := time.Now()
	completed := make([]Step, 0, len(steps))

	for _, s := range steps {
		if err := withRetry(ctx, retryEach, s.Forward); err != nil {
			compErr := compensate(ctx, completed, compensationRetries)
			return Result{
				OK:         false,
				FailedAt:   s.Name,
				Compensated: compErr == nil,
				Latency:    time.Since(start),
			}
		}
		completed = append(completed, s)
	}

	return Result{
		OK:      true,
		Latency: time.Since(start),
	}
}

// compensate walks the completed steps in reverse.
func compensate(ctx context.Context, completed []Step, retries int) error {
	var firstErr error
	for i := len(completed) - 1; i >= 0; i-- {
		s := completed[i]
		if s.Compensate == nil {
			continue
		}
		if err := withRetry(ctx, retries, s.Compensate); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("compensate %q: %w", s.Name, err)
			}
		}
	}
	return firstErr
}

// withRetry runs fn at most attempts+1 times with light backoff. Any
// context cancellation aborts immediately.
func withRetry(ctx context.Context, attempts int, fn func(ctx context.Context) error) error {
	if attempts < 0 {
		attempts = 0
	}
	var lastErr error
	for i := 0; i <= attempts; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		// Light fixed backoff. The lab is intentionally simple.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(50*(i+1)) * time.Millisecond):
		}
	}
	if lastErr == nil {
		return errors.New("saga: retry exhausted")
	}
	return lastErr
}
