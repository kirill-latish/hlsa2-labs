// Package outbox is the transactional-outbox helper. The contract: a
// service's business write AND the events_outbox INSERT commit in the
// same Postgres transaction. The outbox-relay later tails the
// committed-but-unpublished rows, in commit (id) order, and produces
// them to Redpanda. This turns a distributed dual-write into one local
// ACID write plus an asynchronous, at-least-once relay.
package outbox

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hlsa2-labs/lab5-3/internal/events"
)

// Insert writes one event into events_outbox inside tx. aggregate_id
// (order_id) is preserved as the future Kafka key so the relay keeps
// per-entity ordering.
func Insert(ctx context.Context, tx pgx.Tx, e events.Event) error {
	if e.OrderID == "" {
		return fmt.Errorf("outbox: order_id required")
	}
	if e.Type == "" {
		return fmt.Errorf("outbox: type required")
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO events_outbox (event_id, aggregate_id, event_type, payload)
		 VALUES ($1, $2, $3, $4::jsonb)
		 ON CONFLICT (event_id) DO NOTHING`,
		e.EventID, e.OrderID, e.Type, string(e.Marshal()),
	)
	if err != nil {
		return fmt.Errorf("outbox: insert: %w", err)
	}
	return nil
}

// Unpublished is a row the relay still needs to ship.
type Unpublished struct {
	RowID   int64
	Payload []byte
	Key     string
	EventID string
	Type    string
}

// FetchUnpublished returns unpublished rows ordered by id (commit
// order). The ORDER BY is what preserves commit order on the wire.
func FetchUnpublished(ctx context.Context, q pgx.Tx, limit int) ([]Unpublished, error) {
	rows, err := q.Query(ctx,
		`SELECT id, event_id, aggregate_id, event_type, payload
		 FROM events_outbox
		 WHERE published_at IS NULL
		 ORDER BY id ASC
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Unpublished
	for rows.Next() {
		var u Unpublished
		if err := rows.Scan(&u.RowID, &u.EventID, &u.Key, &u.Type, &u.Payload); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// MarkPublished stamps published_at on a batch of rows.
func MarkPublished(ctx context.Context, q pgx.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := q.Exec(ctx,
		`UPDATE events_outbox SET published_at = now() WHERE id = ANY($1)`,
		ids,
	)
	return err
}

// Backlog returns (count, ageSeconds) for unpublished rows. ageSeconds
// is the age of the oldest unpublished row, 0 if the backlog is empty.
func Backlog(ctx context.Context, pool *pgxpool.Pool) (int64, float64, error) {
	var count int64
	var age float64
	err := pool.QueryRow(ctx,
		`SELECT count(*),
		        COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at))), 0)
		 FROM events_outbox WHERE published_at IS NULL`,
	).Scan(&count, &age)
	return count, age, err
}
