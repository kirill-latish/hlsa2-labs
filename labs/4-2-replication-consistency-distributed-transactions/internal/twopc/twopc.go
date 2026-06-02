// Package twopc orchestrates an XA-style two-phase commit across
// multiple HTTP-fronted Postgres-backed participants. Each participant
// implements three endpoints: /xa/prepare, /xa/commit, /xa/abort; the
// participant uses Postgres `PREPARE TRANSACTION <gid>` semantics to
// hold its locks until the orchestrator decides commit-or-abort.
//
// This is deliberately a teaching implementation: it shows the
// in-doubt window, the lock-hold cost, and what happens when the
// commit-decision message can't reach a participant. Production XA
// requires durable coordinator state and persistent recovery - which
// is exactly what topic 248 says you should avoid.
package twopc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Participant is one HTTP-fronted XA participant.
type Participant struct {
	Name   string
	URL    string // e.g. http://payment-svc:8081
	Client *http.Client
}

// Result is the outcome of one place-order via 2PC.
type Result struct {
	OK         bool
	GID        string
	FailedAt   string // "prepare:<svc>" or "commit:<svc>" or "abort:<svc>"
	Latency    time.Duration
	Recovered  bool // an abort cleaned the in-doubt window cleanly
}

// PreparePayload is the JSON each participant sees on /xa/prepare.
type PreparePayload struct {
	OrderID string `json:"order_id"`
	GID     string `json:"gid"`
	// Caller fills in only the participant-relevant fields.
	UserID   string `json:"user_id,omitempty"`
	Amount   int64  `json:"amount,omitempty"`
	SKU      string `json:"sku,omitempty"`
	Quantity int    `json:"quantity,omitempty"`
	Address  string `json:"address,omitempty"`
}

// PerParticipant maps a participant name to its prepare payload.
type PerParticipant struct {
	P       Participant
	Payload PreparePayload
}

// Run executes prepare across all participants then commit-or-abort.
func Run(ctx context.Context, gid string, parts []PerParticipant, prepareTimeout, commitTimeout time.Duration) Result {
	start := time.Now()

	prepareCtx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()

	// Phase 1: prepare. If any prepare fails, abort everything we did
	// prepare on the way out.
	preparedSoFar := make([]Participant, 0, len(parts))
	for _, pp := range parts {
		if err := postJSON(prepareCtx, pp.P, "/xa/prepare", pp.Payload); err != nil {
			abortAll(context.Background(), preparedSoFar, gid, commitTimeout)
			return Result{
				OK:        false,
				GID:       gid,
				FailedAt:  "prepare:" + pp.P.Name,
				Latency:   time.Since(start),
				Recovered: true,
			}
		}
		preparedSoFar = append(preparedSoFar, pp.P)
	}

	// Phase 2: commit. If any commit hangs/fails, abort the rest -
	// production XA would persist the decision and retry; we surface
	// the in-doubt window instead, which is the lesson.
	commitCtx, cancel2 := context.WithTimeout(ctx, commitTimeout)
	defer cancel2()
	for _, p := range preparedSoFar {
		if err := postJSON(commitCtx, p, "/xa/commit", map[string]string{"gid": gid}); err != nil {
			// In-doubt: leave the others prepared. Surface failure to
			// the caller so the bench logs it. The participants stay
			// holding locks until somebody (a watchdog, a human) issues
			// the abort. The lab dashboard charts this as in-doubt count
			// and lock-hold p99.
			return Result{
				OK:       false,
				GID:      gid,
				FailedAt: "commit:" + p.Name,
				Latency:  time.Since(start),
			}
		}
	}

	return Result{OK: true, GID: gid, Latency: time.Since(start)}
}

func abortAll(ctx context.Context, parts []Participant, gid string, timeout time.Duration) {
	abortCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, p := range parts {
		_ = postJSON(abortCtx, p, "/xa/abort", map[string]string{"gid": gid})
	}
}

func postJSON(ctx context.Context, p Participant, path string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	cli := p.Client
	if cli == nil {
		cli = http.DefaultClient
	}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("twopc: %s %s -> %d: %s", p.Name, path, resp.StatusCode, string(raw))
	}
	// Optionally decode for shape validation; ignore otherwise.
	var ack struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&ack)
	if !ack.OK && ack.Error != "" {
		return errors.New(ack.Error)
	}
	return nil
}
