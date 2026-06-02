// Package lsn is a tiny helper around Postgres Log Sequence Numbers.
// Used by lag-sampler and raw-bench's lsn-wait policy. The wire format
// is "XXXXXXXX/XXXXXXXX" (32-bit segment / 32-bit byte offset).
package lsn

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// LSN is an absolute byte offset into the WAL stream.
type LSN uint64

// Parse converts the textual Postgres representation to an LSN.
func Parse(text string) (LSN, error) {
	parts := strings.Split(strings.TrimSpace(text), "/")
	if len(parts) != 2 {
		return 0, fmt.Errorf("lsn: bad format %q", text)
	}
	hi, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("lsn: parse high half: %w", err)
	}
	lo, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("lsn: parse low half: %w", err)
	}
	return LSN((hi << 32) | lo), nil
}

// String returns the canonical Postgres formatting.
func (l LSN) String() string {
	return fmt.Sprintf("%X/%X", uint32(l>>32), uint32(l))
}

// Bytes returns the absolute byte offset.
func (l LSN) Bytes() uint64 { return uint64(l) }

// Diff returns positive bytes that primary leads target by, or 0 if
// target has caught up.
func Diff(primary, target LSN) uint64 {
	if primary <= target {
		return 0
	}
	return uint64(primary - target)
}

// CurrentWAL returns the primary's pg_current_wal_lsn().
func CurrentWAL(ctx context.Context, q pgxQuerier) (LSN, error) {
	var s string
	if err := q.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&s); err != nil {
		return 0, err
	}
	return Parse(s)
}

// LastReplay returns a replica's pg_last_wal_replay_lsn() (NULL on
// primary - returns 0 in that case).
func LastReplay(ctx context.Context, q pgxQuerier) (LSN, error) {
	var s *string
	if err := q.QueryRow(ctx, "SELECT pg_last_wal_replay_lsn()::text").Scan(&s); err != nil {
		return 0, err
	}
	if s == nil {
		return 0, nil
	}
	return Parse(*s)
}

// LastReplayTimestamp returns the replica's pg_last_xact_replay_timestamp().
// Returns the zero value if the replica has not yet replayed any commit.
func LastReplayTimestamp(ctx context.Context, q pgxQuerier) (time.Time, error) {
	var t *time.Time
	if err := q.QueryRow(ctx, "SELECT pg_last_xact_replay_timestamp()").Scan(&t); err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// WaitForReplay polls a replica until its LSN catches up to `target`,
// or the timeout fires. Returns the elapsed wait or an error.
func WaitForReplay(ctx context.Context, q pgxQuerier, target LSN, timeout, pollEvery time.Duration) (time.Duration, error) {
	if pollEvery <= 0 {
		pollEvery = 5 * time.Millisecond
	}
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		got, err := LastReplay(ctx, q)
		if err == nil && got >= target {
			return time.Since(start), nil
		}
		if time.Now().After(deadline) {
			return time.Since(start), fmt.Errorf("lsn: timed out waiting for %s", target)
		}
		select {
		case <-ctx.Done():
			return time.Since(start), ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

// pgxQuerier is the subset of pgx.Conn / pgxpool.Pool we need. Stays
// tiny so tests can mock it cheaply if needed.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
