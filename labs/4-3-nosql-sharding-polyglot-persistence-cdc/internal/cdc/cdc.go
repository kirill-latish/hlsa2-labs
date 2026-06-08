// Package cdc decodes the Debezium pgoutput JSON envelope (with the
// ExtractNewRecordState transform applied) into typed records.
//
// The connector config in debezium/connector-postgres.json keeps
// `unwrap.delete.handling.mode=rewrite`, which means tombstone events
// arrive as a row with `__deleted=true` and the row's previous values.
// The lab indexes products/users/orders by primary key, so for the
// happy path we only need the post-image plus a couple of metadata
// fields (`__lsn`, `__source_ts_ms`, `__op`).
package cdc

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Envelope is the shape Debezium emits with our transform configuration.
type Envelope struct {
	ID           any    `json:"id"`
	Op           string `json:"__op"`
	LSN          any    `json:"__source_lsn"`
	SourceTSMS   int64  `json:"__source_ts_ms"`
	TxID         any    `json:"__source_txId"`
	Deleted      string `json:"__deleted,omitempty"`
	TenantID     string `json:"tenant_id,omitempty"`
	Email        string `json:"email,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	SKU          string `json:"sku,omitempty"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
	PriceCents   int64  `json:"price_cents,omitempty"`
	Stock        int    `json:"stock,omitempty"`
	SearchFacets any    `json:"search_facets,omitempty"`
	UserID       any    `json:"user_id,omitempty"`
	ProductID    any    `json:"product_id,omitempty"`
	Quantity     int    `json:"quantity,omitempty"`
	TotalCents   int64  `json:"total_cents,omitempty"`
	Status       string `json:"status,omitempty"`
	CommittedAt  any    `json:"committed_at,omitempty"`
	UpdatedAt    any    `json:"updated_at,omitempty"`
}

// Decode parses a Debezium-emitted JSON message.
func Decode(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return e, err
	}
	return e, nil
}

// SourceTime is the wall-clock time Postgres committed the row,
// according to Debezium.
func (e Envelope) SourceTime() time.Time {
	if e.SourceTSMS <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(e.SourceTSMS).UTC()
}

// IDInt64 coerces the typed `id` field into int64.
func (e Envelope) IDInt64() (int64, error) {
	return coerceInt64(e.ID)
}

// LSNString returns the LSN as a "0/00000000" style string.
func (e Envelope) LSNString() string {
	switch v := e.LSN.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		// Connect emits numerics as float64 by default. Cast back.
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return fmt.Sprint(v)
	}
}

// IsDelete reports a delete event (after rewrite handling).
func (e Envelope) IsDelete() bool { return e.Deleted == "true" || e.Op == "d" }

// CommittedAtTime returns committed_at as a time.Time, accommodating
// either an ISO-8601 string or a millisecond epoch.
func (e Envelope) CommittedAtTime() time.Time {
	switch v := e.CommittedAt.(type) {
	case string:
		t, err := time.Parse(time.RFC3339Nano, v)
		if err == nil {
			return t.UTC()
		}
		t2, err := time.Parse("2006-01-02T15:04:05.999999Z", v)
		if err == nil {
			return t2.UTC()
		}
	case float64:
		return time.UnixMilli(int64(v)).UTC()
	}
	return time.Time{}
}

func coerceInt64(v any) (int64, error) {
	switch t := v.(type) {
	case nil:
		return 0, fmt.Errorf("nil id")
	case float64:
		return int64(t), nil
	case int64:
		return t, nil
	case string:
		return strconv.ParseInt(t, 10, 64)
	}
	return 0, fmt.Errorf("unhandled id type %T", v)
}
