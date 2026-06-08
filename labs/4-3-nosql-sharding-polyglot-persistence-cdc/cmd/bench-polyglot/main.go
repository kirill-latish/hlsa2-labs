// bench-polyglot drives a write-then-read workload to demonstrate the
// three freshness policies. Each "request":
//
//  1. UPDATE products SET price_cents = price_cents + 1 RETURNING ... pg_current_wal_lsn()
//  2. Apply the configured policy via internal/freshness.Read
//
// Records the policy outcome (source/stale/waited/violations) into
// summary.json + per-row CSV.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hlsa2-labs/lab4-3/internal/freshness"
	"github.com/hlsa2-labs/lab4-3/internal/svchelp"
)

type pgReader struct{ pg *pgxpool.Pool }

func (r pgReader) ReadFromSoR(ctx context.Context, productID int64) (string, string, error) {
	var (
		price int64
		stock int
		lsn   string
	)
	err := r.pg.QueryRow(ctx, `
        SELECT price_cents, stock, pg_current_wal_lsn()::text
          FROM products WHERE id = $1
    `, productID).Scan(&price, &stock, &lsn)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("price=%d/stock=%d", price, stock), lsn, nil
}

type esReader struct {
	hc      *http.Client
	baseURL string
}

func (r esReader) ReadFromDerived(ctx context.Context, productID int64) (string, string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/products/_doc/%d", r.baseURL, productID), nil)
	resp, err := r.hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var doc struct {
		Source struct {
			PriceCents int64  `json:"price_cents"`
			Stock      int    `json:"stock"`
			LSN        string `json:"_lsn"`
		} `json:"_source"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", "", err
	}
	return fmt.Sprintf("price=%d/stock=%d", doc.Source.PriceCents, doc.Source.Stock), doc.Source.LSN, nil
}

type esLagSampler struct {
	hc      *http.Client
	baseURL string

	mu   sync.RWMutex
	lsn  string
	last time.Time
}

func (l *esLagSampler) IndexedLSN(ctx context.Context) (string, error) {
	l.mu.RLock()
	if time.Since(l.last) < 100*time.Millisecond {
		v := l.lsn
		l.mu.RUnlock()
		return v, nil
	}
	l.mu.RUnlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.last) < 100*time.Millisecond {
		return l.lsn, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, l.baseURL+"/indexed-lsn", nil)
	resp, err := l.hc.Do(req)
	if err != nil {
		return l.lsn, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var d struct {
		LSN string `json:"lsn"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return l.lsn, err
	}
	l.lsn = d.LSN
	l.last = time.Now()
	return l.lsn, nil
}

func main() {
	pgDSN := svchelp.EnvOrDefault("POSTGRES_DSN", "postgres://hlsa:hlsa@postgres:5432/hlsa?sslmode=disable")
	esURL := svchelp.EnvOrDefault("ES_URL", "http://elasticsearch:9200")
	esConsumerURL := svchelp.EnvOrDefault("ES_CONSUMER_URL", "http://es-consumer:9000")
	outDir := flag.String("out", svchelp.EnvOrDefault("OUT_DIR", "/perf"), "output directory")
	freshnessFlag := flag.String("freshness", svchelp.EnvOrDefault("FRESHNESS", "lsn-wait"), "policy")
	rate := flag.Int("rate", svchelp.EnvIntOrDefault("WRITE_RATE", 50), "requests per second")
	durSec := flag.Int("duration-seconds", svchelp.EnvIntOrDefault("DURATION_SECONDS", 60), "duration in seconds")
	warmup := flag.Int("warmup-seconds", svchelp.EnvIntOrDefault("WARMUP_S", 5), "warmup seconds")
	maxWait := flag.Int("lsn-wait-max-ms", svchelp.EnvIntOrDefault("LSN_WAIT_MAX_MS", 1500), "max wait ms for lsn-wait")
	flag.Parse()

	policy, err := freshness.Parse(*freshnessFlag)
	if err != nil {
		log.Fatalf("bench-polyglot: %v", err)
	}

	ctx, cancel := svchelp.SignalContext()
	defer cancel()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("bench-polyglot: mkdir %s: %v", *outDir, err)
	}

	pg, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("bench-polyglot: pg: %v", err)
	}
	defer pg.Close()

	hc := &http.Client{Timeout: 5 * time.Second}
	reader := freshness.Reader{
		SoR:          pgReader{pg: pg},
		Derived:      esReader{hc: hc, baseURL: esURL},
		Lag:          &esLagSampler{hc: hc, baseURL: esConsumerURL},
		LSNWaitMax:   time.Duration(*maxWait) * time.Millisecond,
		PollInterval: 25 * time.Millisecond,
	}

	csvPath := filepath.Join(*outDir, "polyglot_samples.csv")
	f, err := os.Create(csvPath)
	if err != nil {
		log.Fatalf("bench-polyglot: create %s: %v", csvPath, err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"product_id", "policy", "source", "stale", "waited_ms", "outcome"})

	if *warmup > 0 {
		log.Printf("bench-polyglot: warmup %ds", *warmup)
		time.Sleep(time.Duration(*warmup) * time.Second)
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	tickInterval := time.Second / time.Duration(*rate)
	if tickInterval <= 0 {
		tickInterval = time.Millisecond
	}
	deadline := time.Now().Add(time.Duration(*durSec) * time.Second)

	var (
		mu          sync.Mutex
		total       int
		violations  int
		errCount    int
		totalWaited time.Duration
		fallbacks   int
	)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			break
		default:
		}
		id := int64(rng.Intn(100) + 1)
		ctxRow, cancelRow := context.WithTimeout(ctx, 10*time.Second)
		var lsn string
		err := pg.QueryRow(ctxRow, `
            UPDATE products
               SET price_cents = price_cents + 1,
                   updated_at  = now(),
                   committed_at = now()
             WHERE id = $1
            RETURNING pg_current_wal_lsn()::text
        `, id).Scan(&lsn)
		if err != nil {
			cancelRow()
			mu.Lock()
			errCount++
			mu.Unlock()
			continue
		}

		dec, err := reader.Read(ctxRow, policy, id, lsn)
		cancelRow()
		mu.Lock()
		total++
		if err != nil {
			errCount++
			mu.Unlock()
			_ = w.Write([]string{
				strconv.FormatInt(id, 10), string(policy), "err", "true", "0", err.Error(),
			})
			continue
		}
		if dec.Stale {
			violations++
		}
		if dec.Source == "fallback-sor" {
			fallbacks++
		}
		totalWaited += dec.Waited
		mu.Unlock()
		_ = w.Write([]string{
			strconv.FormatInt(id, 10),
			string(policy),
			dec.Source,
			strconv.FormatBool(dec.Stale),
			strconv.FormatInt(dec.Waited.Milliseconds(), 10),
			"ok",
		})
		if tickInterval > 0 {
			time.Sleep(tickInterval)
		}
	}

	w.Flush()

	avgWaited := time.Duration(0)
	if total > 0 {
		avgWaited = totalWaited / time.Duration(total)
	}

	summary := map[string]any{
		"freshness":        string(policy),
		"rate":             *rate,
		"duration":         (time.Duration(*durSec) * time.Second).String(),
		"total":            total,
		"violations":       violations,
		"violations_pct":   pctFloat(violations, total),
		"fallbacks_to_sor": fallbacks,
		"errors":           errCount,
		"avg_waited_ms":    avgWaited.Milliseconds(),
		"lsn_wait_max_ms":  *maxWait,
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	_ = os.WriteFile(filepath.Join(*outDir, "summary.json"), b, 0o644)

	log.Printf("bench-polyglot: policy=%s total=%d violations=%d (%.2f%%) fallbacks=%d errors=%d",
		policy, total, violations, pctFloat(violations, total), fallbacks, errCount)
}

func pctFloat(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}
