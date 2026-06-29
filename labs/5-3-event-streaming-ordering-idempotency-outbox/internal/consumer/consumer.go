// Package consumer holds the event-application semantics that the
// idempotency, ordering, and replay experiments hinge on. The two
// consumer modes (naive vs idempotent) and the three replay modes
// (off / rebuild-only / reprocess) all funnel through Apply so the
// behaviour stays in one auditable place.
package consumer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hlsa2-labs/lab5-3/internal/events"
)

// Mode selects whether the consumer dedups by event_id.
type Mode string

const (
	ModeNaive      Mode = "naive-consumer"
	ModeIdempotent Mode = "idempotent-consumer"
)

// ReplayMode selects normal processing vs a rebuild that suppresses
// external side effects vs a deliberate reprocess that re-fires them.
type ReplayMode string

const (
	ReplayOff         ReplayMode = "off"
	ReplayRebuildOnly ReplayMode = "rebuild-only"
	ReplayReprocess   ReplayMode = "reprocess"
)

// Valid reports whether m is a known consumer mode.
func (m Mode) Valid() bool { return m == ModeNaive || m == ModeIdempotent }

// Valid reports whether r is a known replay mode.
func (r ReplayMode) Valid() bool {
	return r == ReplayOff || r == ReplayRebuildOnly || r == ReplayReprocess
}

// Result is what Apply observed for one event so the caller can update
// Prometheus counters.
type Result struct {
	Duplicate         bool // suppressed by dedup
	OrderingViolation bool // arrived with seq < already-applied seq
	SideEffectFired   bool // an external side effect was inserted
}

// Apply runs one event through the consumer under (mode, replay). The
// dedup marker and the projection update and the side-effect insert
// all commit in ONE transaction, so a crash between them can never
// leave the event half-applied.
func Apply(ctx context.Context, pool *pgxpool.Pool, mode Mode, replay ReplayMode, e events.Event) (Result, error) {
	if e.OrderID == "" {
		return Result{}, errors.New("consumer: empty order_id")
	}
	var res Result

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Dedup is only consulted in normal processing. During a replay we
	// intentionally re-apply to the projection regardless (rebuild),
	// and reprocess deliberately re-fires effects to show the footgun.
	if replay == ReplayOff && mode == ModeIdempotent {
		tag, err := tx.Exec(ctx,
			`INSERT INTO processed_ids (event_id) VALUES ($1) ON CONFLICT (event_id) DO NOTHING`,
			e.EventID,
		)
		if err != nil {
			return res, fmt.Errorf("consumer: dedup insert: %w", err)
		}
		if tag.RowsAffected() == 0 {
			res.Duplicate = true
			return res, tx.Commit(ctx)
		}
	}

	// Ordering check: an event whose seq is below the highest seq we
	// already applied for this entity arrived out of order.
	var lastSeq int64
	err = tx.QueryRow(ctx,
		`SELECT last_seq FROM projection WHERE order_id = $1 FOR UPDATE`, e.OrderID,
	).Scan(&lastSeq)
	switch {
	case err == nil:
		if e.Seq < lastSeq {
			res.OrderingViolation = true
		}
	case errors.Is(err, pgx.ErrNoRows):
		// first event for this entity
	default:
		return res, fmt.Errorf("consumer: read projection: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO projection (order_id, last_seq, status, amount, events_applied)
		 VALUES ($1, $2, $3, $4, 1)
		 ON CONFLICT (order_id) DO UPDATE SET
		   last_seq        = GREATEST(projection.last_seq, EXCLUDED.last_seq),
		   status          = EXCLUDED.status,
		   amount          = projection.amount + EXCLUDED.amount,
		   events_applied  = projection.events_applied + 1,
		   updated_at      = now()`,
		e.OrderID, e.Seq, e.Type, e.Amount,
	); err != nil {
		return res, fmt.Errorf("consumer: projection upsert: %w", err)
	}

	// External side effects fire in normal and reprocess modes, but are
	// suppressed in rebuild-only: that is the replay-to-rebuild vs
	// replay-to-reprocess distinction the lab teaches.
	if replay != ReplayRebuildOnly {
		if _, err := tx.Exec(ctx,
			`INSERT INTO side_effects (order_id, event_id, kind) VALUES ($1, $2, 'notify')`,
			e.OrderID, e.EventID,
		); err != nil {
			return res, fmt.Errorf("consumer: side effect: %w", err)
		}
		res.SideEffectFired = true
	}

	return res, tx.Commit(ctx)
}
