// bench-cdc-lag drives a fixed-rate stream of products updates into
// Postgres, polls Elasticsearch for the resulting document, and writes
// per-row lag samples to lag_samples.csv inside OUT_DIR.
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

	"github.com/hlsa2-labs/lab4-3/internal/svchelp"
)

func main() {
	pgDSN := svchelp.EnvOrDefault("POSTGRES_DSN", "postgres://hlsa:hlsa@postgres:5432/hlsa?sslmode=disable")
	esURL := svchelp.EnvOrDefault("ES_URL", "http://elasticsearch:9200")
	outDir := flag.String("out", svchelp.EnvOrDefault("OUT_DIR", "/perf"), "output directory")
	rate := flag.Int("rate", svchelp.EnvIntOrDefault("WRITE_RATE", 100), "writes per second")
	durSec := flag.Int("duration-seconds", svchelp.EnvIntOrDefault("DURATION_SECONDS", 60), "duration in seconds")
	warmup := flag.Int("warmup-seconds", svchelp.EnvIntOrDefault("WARMUP_S", 5), "warmup seconds")
	pollMS := flag.Int("poll-ms", svchelp.EnvIntOrDefault("POLL_INTERVAL_MS", 25), "poll interval ms")
	timeoutMS := flag.Int("timeout-ms", svchelp.EnvIntOrDefault("POLL_TIMEOUT_MS", 30000), "max wait per row in ms")
	label := flag.String("label", svchelp.EnvOrDefault("LABEL", "base"), "label written into summary.json")
	flag.Parse()

	ctx, cancel := svchelp.SignalContext()
	defer cancel()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("bench-cdc-lag: mkdir %s: %v", *outDir, err)
	}

	pg, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("bench-cdc-lag: pg connect: %v", err)
	}
	defer pg.Close()

	csvPath := filepath.Join(*outDir, "lag_samples.csv")
	f, err := os.Create(csvPath)
	if err != nil {
		log.Fatalf("bench-cdc-lag: create %s: %v", csvPath, err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"product_id", "committed_at", "indexed_at", "lag_ms", "outcome"})

	if *warmup > 0 {
		log.Printf("bench-cdc-lag: warmup %ds", *warmup)
		time.Sleep(time.Duration(*warmup) * time.Second)
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	tickInterval := time.Second / time.Duration(*rate)
	if tickInterval <= 0 {
		tickInterval = time.Millisecond
	}
	deadline := time.Now().Add(time.Duration(*durSec) * time.Second)
	var (
		mu        sync.Mutex
		writeOK   int
		hits      int
		timeouts  int
		errors    int
		totalLag  time.Duration
	)
	hc := &http.Client{Timeout: time.Duration(*timeoutMS+5000) * time.Millisecond}
	wg := sync.WaitGroup{}
	work := make(chan int64, 64)

	const workers = 16
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				lag, indexedAt, status := pollUntilIndexed(ctx, hc, esURL, id, *pollMS, *timeoutMS)
				mu.Lock()
				switch status {
				case "ok":
					hits++
					totalLag += lag
				case "timeout":
					timeouts++
				default:
					errors++
				}
				mu.Unlock()
				_ = w.Write([]string{
					strconv.FormatInt(id, 10),
					"-",
					indexedAt,
					strconv.FormatInt(lag.Milliseconds(), 10),
					status,
				})
			}
		}()
	}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			break
		default:
		}
		// Pick an existing product_id (1..100 seeded by seed.sh) and
		// bump its updated_at + price_cents so Debezium emits a row.
		id := int64(rng.Intn(100) + 1)
		var committedAt time.Time
		err := pg.QueryRow(ctx, `
            UPDATE products
               SET price_cents = price_cents + 1,
                   updated_at  = now(),
                   committed_at = now()
             WHERE id = $1
            RETURNING committed_at
        `, id).Scan(&committedAt)
		if err != nil {
			mu.Lock()
			errors++
			mu.Unlock()
			continue
		}
		mu.Lock()
		writeOK++
		mu.Unlock()
		work <- id
		if tickInterval > 0 {
			time.Sleep(tickInterval)
		}
	}
	close(work)
	wg.Wait()

	w.Flush()
	mean := time.Duration(0)
	if hits > 0 {
		mean = totalLag / time.Duration(hits)
	}

	summary := map[string]any{
		"label":        *label,
		"rate":         *rate,
		"duration":     (time.Duration(*durSec) * time.Second).String(),
		"writes_ok":    writeOK,
		"hits":         hits,
		"timeouts":     timeouts,
		"errors":       errors,
		"mean_lag_ms":  mean.Milliseconds(),
		"started_at":   deadline.Add(-time.Duration(*durSec) * time.Second).UTC().Format(time.RFC3339),
		"ended_at":     time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	_ = os.WriteFile(filepath.Join(*outDir, "summary.json"), b, 0o644)

	log.Printf("bench-cdc-lag: writes_ok=%d hits=%d timeouts=%d errors=%d mean_lag=%s",
		writeOK, hits, timeouts, errors, mean)
}

// pollUntilIndexed polls /products/_doc/<id> until the document's
// _committed_at advances past the moment we issued the write. We don't
// actually require committed_at equality — the indexer always copies
// committed_at into the document, so a successful 200 with a
// committed_at >= our pre-write timestamp is enough.
//
// The function returns the lag (now() - committed_at), the indexed_at
// stamp the consumer attached, and a status of "ok" | "timeout" | "err".
func pollUntilIndexed(ctx context.Context, hc *http.Client, esURL string, id int64, pollMS, timeoutMS int) (time.Duration, string, string) {
	start := time.Now()
	deadline := start.Add(time.Duration(timeoutMS) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return 0, "", "err"
		default:
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("%s/products/_doc/%d", esURL, id), nil)
		resp, err := hc.Do(req)
		if err != nil {
			time.Sleep(time.Duration(pollMS) * time.Millisecond)
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			time.Sleep(time.Duration(pollMS) * time.Millisecond)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return 0, "", "err"
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var doc struct {
			Source struct {
				CommittedAt string `json:"committed_at"`
				IndexedAt   string `json:"_indexed_at"`
			} `json:"_source"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return 0, "", "err"
		}
		ca, err := time.Parse(time.RFC3339Nano, doc.Source.CommittedAt)
		if err != nil {
			ca, err = time.Parse(time.RFC3339, doc.Source.CommittedAt)
		}
		if err != nil {
			time.Sleep(time.Duration(pollMS) * time.Millisecond)
			continue
		}
		if ca.Before(start) {
			time.Sleep(time.Duration(pollMS) * time.Millisecond)
			continue
		}
		return time.Since(ca), doc.Source.IndexedAt, "ok"
	}
	return time.Since(start), "", "timeout"
}
