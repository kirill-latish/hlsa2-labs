// loadgen-saga drives the orchestrator at a steady offered rate for a
// fixed duration. Used by both bench-saga and bench-2pc; the only
// difference is the ?mode= URL query parameter on the orchestrator.
//
// At the end it writes summary.json with success-rate, p50/p95/p99/p99.9
// latency, and the count of saga compensations vs 2PC aborts that
// surfaced in the failure path.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/hlsa2-labs/lab4-2/internal/payloads"
)

func main() {
	mode := flag.String("mode", "saga", "saga|2pc")
	url := flag.String("url", os.Getenv("ORCHESTRATOR_URL"), "orchestrator URL, e.g. http://orchestrator:8080")
	rate := flag.Int("rate", 50, "place-order requests per second")
	duration := flag.Duration("duration", 60*time.Second, "test duration")
	warmup := flag.Duration("warmup", 5*time.Second, "warmup before measurement")
	workers := flag.Int("workers", 32, "concurrent worker goroutines")
	out := flag.String("out", "./perf/results/saga/run", "output directory for summary.json")
	flag.Parse()

	if *url == "" {
		*url = "http://orchestrator:8080"
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatalf("mkdir out: %v", err)
	}

	hc := &http.Client{Timeout: 30 * time.Second}
	endpoint := *url + "/place-order?mode=" + *mode

	totalDuration := *warmup + *duration
	ctx, cancel := context.WithTimeout(context.Background(), totalDuration+10*time.Second)
	defer cancel()

	stats := newLoadStats()

	perWorkerInterval := time.Duration(int64(time.Second) * int64(*workers) / int64(*rate))
	if perWorkerInterval <= 0 {
		perWorkerInterval = time.Millisecond
	}
	end := time.Now().Add(totalDuration)
	measureStart := time.Now().Add(*warmup)

	log.Printf("loadgen-saga: mode=%s url=%s rate=%d duration=%s workers=%d", *mode, endpoint, *rate, *duration, *workers)

	wg := sync.WaitGroup{}
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)*9973))
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
					// Randomize user/sku per request across the seeded
					// pool of 50. This keeps contention low so 2PC's
					// in-doubt window only opens when faults are
					// actually injected, not just from natural lock
					// queueing.
					n := rng.Intn(50) + 1
					req := payloads.PlaceOrderRequest{
						OrderID:  fmt.Sprintf("order-%s", uuid.NewString()),
						UserID:   fmt.Sprintf("user-%d", n),
						SKU:      fmt.Sprintf("sku-%d", n),
						Quantity: 1,
						Amount:   100,
						Address:  "1 Test Street",
					}
					stats.runOne(ctx, hc, endpoint, req, *mode, counted)
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
	log.Printf("loadgen-saga: wrote %s/summary.json mode=%s requests=%d ok=%d failed=%d (rate=%.4f)",
		*out, summary.Mode, summary.Requests, summary.OK, summary.Failed, summary.SuccessRate)
}

// ----------------------------------------------------------------
// Stats
// ----------------------------------------------------------------

type loadStats struct {
	mu       sync.Mutex
	latency  []float64
	requests int64
	ok       int64
	failed   int64
	failedAt map[string]int64
	compensated int64
}

func newLoadStats() *loadStats { return &loadStats{failedAt: make(map[string]int64)} }

func (s *loadStats) runOne(ctx context.Context, hc *http.Client, url string, req payloads.PlaceOrderRequest, mode string, counted bool) {
	buf, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := hc.Do(httpReq)
	if err != nil {
		if counted {
			s.recordError("transport")
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		_, _ = io.Copy(io.Discard, resp.Body)
		if counted {
			s.recordError(fmt.Sprintf("http_%d", resp.StatusCode))
		}
		return
	}
	var body payloads.PlaceOrderResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		if counted {
			s.recordError("decode")
		}
		return
	}
	if !counted {
		return
	}
	s.recordRequest(time.Since(start))
	if body.Status == "completed" {
		atomic.AddInt64(&s.ok, 1)
	} else {
		atomic.AddInt64(&s.failed, 1)
		s.mu.Lock()
		s.failedAt[body.FailedAt]++
		if body.Compensated {
			s.compensated++
		}
		s.mu.Unlock()
	}
	_ = mode
}

func (s *loadStats) recordRequest(lat time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	atomic.AddInt64(&s.requests, 1)
	s.latency = append(s.latency, float64(lat.Microseconds())/1000.0)
}

func (s *loadStats) recordError(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	atomic.AddInt64(&s.requests, 1)
	atomic.AddInt64(&s.failed, 1)
	s.failedAt[reason]++
}

type Summary struct {
	Mode        string             `json:"mode"`
	TargetRate  int                `json:"target_rate_per_s"`
	DurationS   int                `json:"duration_s"`
	Requests    int64              `json:"requests"`
	OK          int64              `json:"ok"`
	Failed      int64              `json:"failed"`
	SuccessRate float64            `json:"success_rate"`
	Compensated int64              `json:"compensated_count"`
	FailedAt    map[string]int64   `json:"failed_at"`
	LatencyMS   map[string]float64 `json:"latency_ms"`
	WrittenAt   string             `json:"written_at"`
}

func (s *loadStats) snapshot(mode string, rate int, duration time.Duration) Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Summary{
		Mode:       mode,
		TargetRate: rate,
		DurationS:  int(duration.Seconds()),
		Requests:   s.requests,
		OK:         s.ok,
		Failed:     s.failed,
		FailedAt:   copyCounts(s.failedAt),
		Compensated: s.compensated,
		LatencyMS:  percentiles(s.latency),
	}
	if s.requests > 0 {
		out.SuccessRate = float64(s.ok) / float64(s.requests)
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
