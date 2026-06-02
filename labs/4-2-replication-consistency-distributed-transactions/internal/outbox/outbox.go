// Package outbox is the transactional-outbox helper used by every
// saga participant. The contract: the participant's business write
// AND the outbox INSERT happen in the same Postgres transaction. The
// outbox-relay later tails the unpublished rows and produces them to
// Redpanda.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Event is the row inserted into events_outbox.
type Event struct {
	EventID     string         // dedupe key for the consumer
	AggregateID string         // order_id
	Type        string         // e.g. "payment.charged"
	Payload     map[string]any // event body, JSONB-encoded
}

// Insert writes the event into events_outbox inside the supplied
// transaction. Returns the event_id (caller should also use it as the
// Kafka key for partition stickiness).
func Insert(ctx context.Context, tx pgx.Tx, e Event) (string, error) {
	if e.EventID == "" {
		e.EventID = uuid.NewString()
	}
	if e.AggregateID == "" {
		return "", fmt.Errorf("outbox: aggregate_id required")
	}
	if e.Type == "" {
		return "", fmt.Errorf("outbox: type required")
	}

	body, err := json.Marshal(e.Payload)
	if err != nil {
		return "", fmt.Errorf("outbox: marshal payload: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO events_outbox (event_id, aggregate_id, event_type, payload)
		 VALUES ($1, $2, $3, $4::jsonb)`,
		e.EventID, e.AggregateID, e.Type, string(body),
	)
	if err != nil {
		return "", fmt.Errorf("outbox: insert: %w", err)
	}
	return e.EventID, nil
}

// Unpublished is what the relay reads. Limit is the batch size.
type Unpublished struct {
	RowID       int64
	EventID     string
	AggregateID string
	Type        string
	Payload     []byte
}

// FetchUnpublished returns rows where published_at is NULL ordered by
// id. Used by outbox-relay/main.go.
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
		var payload []byte
		if err := rows.Scan(&u.RowID, &u.EventID, &u.AggregateID, &u.Type, &payload); err != nil {
			return nil, err
		}
		u.Payload = payload
		out = append(out, u)
	}
	return out, rows.Err()
}

// MarkPublished stamps published_at on a batch of outbox rows.
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
