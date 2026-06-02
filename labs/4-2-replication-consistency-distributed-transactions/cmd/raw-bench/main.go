// raw-bench is the read-after-write benchmark harness.
//
// It runs in-cluster as a one-shot CLI binary. Per worker:
//
//  1. UPDATE accounts SET balance = NEXTVAL, version = version+1
//     WHERE session_id = $1; capture pg_current_wal_lsn() in the same tx.
//  2. RecordWrite(sessionID, entityKey, writeLSN, now()) on the picker.
//  3. Read via picker.Pick().
//  4. If returned balance < expectedBalance -> violation.
//
// At the end it writes summary.json with violation rate, p50/p95/p99
// latency, and a per-replica histogram count.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hlsa2-labs/lab4-2/internal/lsn"
	"github.com/hlsa2-labs/lab4-2/internal/readpolicy"
)

func main() {
	mode := flag.String("mode", "naive", "naive|session-pin|lsn-wait|primary-read")
	rate := flag.Int("rate", 200, "writes per second across all workers")
	duration := flag.Duration("duration", 30*time.Second, "test duration")
	warmup := flag.Duration("warmup", 5*time.Second, "warmup before measurement")
	workers := flag.Int("workers", 16, "number of concurrent workers")
	out := flag.String("out", "./perf/results/raw/run", "output directory for summary.json")
	primaryDSN := flag.String("primary", os.Getenv("PRIMARY_DSN"), "primary DSN")
	replica1DSN := flag.String("replica1", os.Getenv("REPLICA1_DSN"), "replica 1 DSN")
	replica2DSN := flag.String("replica2", os.Getenv("REPLICA2_DSN"), "replica 2 DSN")
	flag.Parse()

	if *primaryDSN == "" {
		log.Fatal("primary DSN required (--primary or PRIMARY_DSN env)")
	}

	primary := mustPool(*primaryDSN, "primary", 32)
	defer primary.Close()
	r1 := mustPool(*replica1DSN, "replica-1", 16)
	defer r1.Close()
	var replicaEPs []readpolicy.Endpoint
	replicaEPs = append(replicaEPs, readpolicy.Endpoint{Name: "replica-1", Pool: r1})
	if *replica2DSN != "" {
		r2 := mustPool(*replica2DSN, "replica-2", 16)
		defer r2.Close()
		replicaEPs = append(replicaEPs, readpolicy.Endpoint{Name: "replica-2", Pool: r2})
	}
	primaryEP := readpolicy.Endpoint{Name: "primary", Pool: primary}

	picker, err := readpolicy.New(*mode, primaryEP, replicaEPs, readpolicy.DefaultConfig())
	if err != nil {
		log.Fatalf("readpolicy: %v", err)
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatalf("mkdir out: %v", err)
	}

	totalDuration := *warmup + *duration
	ctx, cancel := context.WithTimeout(context.Background(), totalDuration+10*time.Second)
	defer cancel()

	log.Printf("raw-bench: mode=%s rate=%d duration=%s warmup=%s workers=%d", *mode, *rate, *duration, *warmup, *workers)

	stats := newStats()

	// Token bucket for global rate limiting.
	perWorkerInterval := time.Duration(int64(time.Second) * int64(*workers) / int64(*rate))
	if perWorkerInterval <= 0 {
		perWorkerInterval = time.Millisecond
	}

	end := time.Now().Add(totalDuration)
	measureStart := time.Now().Add(*warmup)

	wg := sync.WaitGroup{}
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))
			ticker := time.NewTicker(perWorkerInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if time.Now().After(end) {
						return
					}
					counted := time.Now().After(measureStart)
					_ = runOne(ctx, primary, picker, rng, id, stats, counted)
				}
			}
		}(i)
	}
	wg.Wait()

	summary := stats.snapshot(*mode, *rate, *duration)
	summary.WrittenAt = time.Now().UTC().Format(time.RFC3339)

	enc, err := os.Create(filepath.Join(*out, "summary.json"))
	if err != nil {
		log.Fatalf("create summary: %v", err)
	}
	defer enc.Close()
	jenc := json.NewEncoder(enc)
	jenc.SetIndent("", "  ")
	if err := jenc.Encode(summary); err != nil {
		log.Fatalf("write summary: %v", err)
	}
	log.Printf("raw-bench: wrote %s/summary.json reads=%d violations=%d (rate=%.4f)",
		*out, summary.Reads, summary.Violations, summary.ViolationRate)
}

// runOne does one write+read pair, accounting violations.
func runOne(ctx context.Context, primary *pgxpool.Pool, picker readpolicy.Picker, rng *rand.Rand, workerID int, stats *bench, counted bool) error {
	sessionID := fmt.Sprintf("w%d-s%d", workerID, rng.Intn(1024))
	// Each worker has its own session id but cycles through 1024 entities.
	entityKey := fmt.Sprintf("acc-%s", sessionID)

	tx, err := primary.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Upsert + bump version. The version is what we'll compare on the
	// read: a stale read returns a smaller version.
	var newVersion int64
	err = tx.QueryRow(ctx,
		`INSERT INTO accounts (session_id, balance, version, updated_at)
		 VALUES ($1, 1, 1, now())
		 ON CONFLICT (session_id) DO UPDATE
		 SET balance = accounts.balance + 1, version = accounts.version + 1, updated_at = now()
		 RETURNING version`,
		sessionID,
	).Scan(&newVersion)
	if err != nil {
		return err
	}

	// Capture LSN inside the same tx so RecordWrite has the exact
	// position the consistency check needs.
	var lsnText string
	if err := tx.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&lsnText); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	parsed, _ := lsn.Parse(lsnText)
	picker.RecordWrite(ctx, sessionID, entityKey, parsed, time.Now())

	// Read via the chosen policy.
	readStart := time.Now()
	ep, _, err := picker.Pick(ctx, sessionID, entityKey)
	if err != nil {
		return err
	}
	var observedVersion int64
	err = ep.Pool.QueryRow(ctx,
		`SELECT COALESCE(version, 0) FROM accounts WHERE session_id = $1`, sessionID,
	).Scan(&observedVersion)
	readLatency := time.Since(readStart)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	if !counted {
		return nil
	}
	// observedVersion < newVersion means stale read => violation.
	stats.record(ep.Name, readLatency, observedVersion >= newVersion)
	return nil
}

// ----------------------------------------------------------------
// Stats accumulator
// ----------------------------------------------------------------

type bench struct {
	mu       sync.Mutex
	latency  []float64
	reads    int64
	violations int64
	perEP    map[string]int64
}

func newStats() *bench {
	return &bench{perEP: make(map[string]int64)}
}

func (b *bench) record(epName string, lat time.Duration, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	atomic.AddInt64(&b.reads, 1)
	b.perEP[epName]++
	b.latency = append(b.latency, float64(lat.Microseconds())/1000.0) // store ms
	if !ok {
		atomic.AddInt64(&b.violations, 1)
	}
}

type Summary struct {
	Mode          string             `json:"mode"`
	TargetRate    int                `json:"target_rate_per_s"`
	DurationS     int                `json:"duration_s"`
	Reads         int64              `json:"reads"`
	Violations    int64              `json:"violations"`
	ViolationRate float64            `json:"violation_rate"`
	LatencyMS     map[string]float64 `json:"latency_ms"`
	PerEndpoint   map[string]int64   `json:"per_endpoint_reads"`
	WrittenAt     string             `json:"written_at"`
}

func (b *bench) snapshot(mode string, rate int, duration time.Duration) Summary {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := Summary{
		Mode:        mode,
		TargetRate:  rate,
		DurationS:   int(duration.Seconds()),
		Reads:       b.reads,
		Violations:  b.violations,
		PerEndpoint: copyCounts(b.perEP),
		LatencyMS:   percentiles(b.latency),
	}
	if b.reads > 0 {
		out.ViolationRate = float64(b.violations) / float64(b.reads)
	}
	return out
}

func percentiles(xs []float64) map[string]float64 {
	if len(xs) == 0 {
		return map[string]float64{}
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	pick := func(p float64) float64 {
		idx := int(p*float64(len(sorted)-1) + 0.5)
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return map[string]float64{
		"p50":  pick(0.50),
		"p90":  pick(0.90),
		"p95":  pick(0.95),
		"p99":  pick(0.99),
		"p999": pick(0.999),
		"max":  sorted[len(sorted)-1],
	}
}

func copyCounts(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ----------------------------------------------------------------
// helpers
// ----------------------------------------------------------------

func mustPool(dsn, label string, max int32) *pgxpool.Pool {
	if dsn == "" {
		log.Fatalf("missing DSN for %s", label)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("parse %s: %v", label, err)
	}
	cfg.MaxConns = max
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		log.Fatalf("pool %s: %v", label, err)
	}
	for i := 0; i < 60; i++ {
		if err := p.Ping(context.Background()); err == nil {
			return p
		}
		time.Sleep(time.Second)
	}
	log.Fatalf("timed out connecting to %s", label)
	return nil
}

