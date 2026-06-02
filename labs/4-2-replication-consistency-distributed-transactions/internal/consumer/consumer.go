// Package consumer holds the two consumer modes used in the replay /
// idempotency proof.
//
//	naive       - applies side effects unconditionally
//	idempotent  - guards side effects behind processed_events table
package consumer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Mode is "naive" or "idempotent".
type Mode string

const (
	ModeNaive      Mode = "naive"
	ModeIdempotent Mode = "idempotent"
)

// Apply runs one event through the consumer in `mode`. The
// "side effect" we apply is intentionally a balance increment on the
// payment.accounts.user-1 row - it's the simplest write that breaks
// loudly under double-replay if the consumer isn't idempotent.
func Apply(ctx context.Context, pool *pgxpool.Pool, mode Mode, eventID, userID string, amount int64) error {
	switch mode {
	case ModeIdempotent:
		return applyIdempotent(ctx, pool, eventID, userID, amount)
	case ModeNaive, "":
		return applyNaive(ctx, pool, userID, amount)
	default:
		return fmt.Errorf("consumer: unknown mode %q", mode)
	}
}

// applyIdempotent inserts into processed_events first; if the
// event_id is already there (zero rows affected), the side effect is
// skipped. Both writes happen in the same transaction so a crash
// between them can't leave the event partially applied.
func applyIdempotent(ctx context.Context, pool *pgxpool.Pool, eventID, userID string, amount int64) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`INSERT INTO processed_events (event_id) VALUES ($1)
		 ON CONFLICT (event_id) DO NOTHING`,
		eventID,
	)
	if err != nil {
		return fmt.Errorf("consumer: dedupe insert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Already processed - skip the side effect, commit the empty tx.
		return tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO accounts (user_id, balance) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET balance = accounts.balance + EXCLUDED.balance`,
		userID, amount,
	); err != nil {
		return fmt.Errorf("consumer: side effect: %w", err)
	}
	return tx.Commit(ctx)
}

// applyNaive just adds. Re-running the same event doubles the
// balance. This is the failure mode the lab teaches.
func applyNaive(ctx context.Context, pool *pgxpool.Pool, userID string, amount int64) error {
	if userID == "" {
		return errors.New("consumer: empty user_id")
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO accounts (user_id, balance) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET balance = accounts.balance + EXCLUDED.balance`,
		userID, amount,
	)
	return err
}
