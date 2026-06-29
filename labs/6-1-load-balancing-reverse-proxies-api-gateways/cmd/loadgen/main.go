// loadgen is an in-cluster Go HTTP service that drives the edge-proxy
// at a constant arrival rate with a configurable fast/slow request mix.
// The slow fraction is what makes request costs uneven, so the
// distribution experiment can show round-robin skew vs least-conn
// rebalancing.
//
// HTTP API (same control surface as lab 3-3's loadgen):
//
//	POST   /start   {"rate_rps":200,"duration_s":60,"label":"baseline","slow_ratio":0.3}
//	POST   /stop
//	GET    /state
//	GET    /summary?label=baseline
//	GET    /healthz
//	GET    /metrics
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hlsa2-labs/lab6-1/internal/metrics"
)

type startReq struct {
	RateRPS   int     `json:"rate_rps"`
	DurationS int     `json:"duration_s"`
	Label     string  `json:"label"`
	SlowRatio float64 `json:"slow_ratio"`
}

type runState struct {
	mu        sync.Mutex
	running   bool
	cancel    context.CancelFunc
	label     string
	startedAt time.Time
	endsAt    time.Time
	rateRPS   int
	durationS int
	slowRatio float64

	offered int64
	served  int64
	failed  int64
}

func (s *runState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"running":    s.running,
		"label":      s.label,
		"started_at": s.startedAt.UTC().Format(time.RFC3339Nano),
		"ends_at":    s.endsAt.UTC().Format(time.RFC3339Nano),
		"rate_rps":   s.rateRPS,
		"duration_s": s.durationS,
		"slow_ratio": s.slowRatio,
		"offered":    atomic.LoadInt64(&s.offered),
		"served":     atomic.LoadInt64(&s.served),
		"failed":     atomic.LoadInt64(&s.failed),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	port := envOrDefault("PORT", "8090")
	edgeURL := envOrDefault("EDGE_URL", "http://edge-proxy:8080")
	httpMetrics := metrics.NewHTTPMetrics("loadgen")
	lg := metrics.NewLoadgen()

	state := &runState{}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxConnsPerHost:     1024,
			MaxIdleConnsPerHost: 256,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", metrics.Handler())

	r.Get("/state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state.snapshot())
	})

	r.Get("/summary", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state.snapshot())
	})

	r.Post("/stop", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.cancel != nil {
			state.cancel()
		}
		state.running = false
		_, _ = w.Write([]byte("stopped"))
	})

	r.Post("/start", func(w http.ResponseWriter, req *http.Request) {
		var rq startReq
		if err := json.NewDecoder(req.Body).Decode(&rq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if rq.Label == "" {
			rq.Label = "baseline"
		}
		if rq.RateRPS <= 0 {
			rq.RateRPS = 200
		}
		if rq.DurationS <= 0 {
			rq.DurationS = 60
		}
		if rq.SlowRatio < 0 {
			rq.SlowRatio = 0
		}
		if rq.SlowRatio > 1 {
			rq.SlowRatio = 1
		}

		state.mu.Lock()
		if state.running && state.cancel != nil {
			state.cancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		state.running = true
		state.cancel = cancel
		state.label = rq.Label
		state.startedAt = time.Now()
		state.endsAt = time.Now().Add(time.Duration(rq.DurationS) * time.Second)
		state.rateRPS = rq.RateRPS
		state.durationS = rq.DurationS
		state.slowRatio = rq.SlowRatio
		atomic.StoreInt64(&state.offered, 0)
		atomic.StoreInt64(&state.served, 0)
		atomic.StoreInt64(&state.failed, 0)
		state.mu.Unlock()

		lg.RateRPS.WithLabelValues(rq.Label).Set(float64(rq.RateRPS))

		go drive(ctx, rq, edgeURL, client, lg, state)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state.snapshot())
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("loadgen listening on :%s, target=%s", port, edgeURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// drive enforces the offered arrival rate with a ticker so the offered
// rate matches the configured RPS regardless of how the edge responds.
func drive(ctx context.Context, rq startReq, edgeURL string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	tick := time.Duration(float64(time.Second) / float64(rq.RateRPS))
	if tick <= 0 {
		tick = time.Millisecond
	}
	deadline := time.Now().Add(time.Duration(rq.DurationS) * time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	var wg sync.WaitGroup
	limit := make(chan struct{}, 4096)

	finish := func() {
		wg.Wait()
		state.mu.Lock()
		state.running = false
		state.mu.Unlock()
	}

	for {
		select {
		case <-ctx.Done():
			finish()
			return
		case now := <-t.C:
			if now.After(deadline) {
				finish()
				return
			}
			cost := "fast"
			if rng.Float64() < rq.SlowRatio {
				cost = "slow"
			}
			limit <- struct{}{}
			wg.Add(1)
			go func(cost string) {
				defer wg.Done()
				defer func() { <-limit }()
				do(ctx, rq, cost, edgeURL, client, lg, state)
			}(cost)
		}
	}
}

func do(ctx context.Context, rq startReq, cost, edgeURL string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	atomic.AddInt64(&state.offered, 1)
	lg.OfferedTotal.WithLabelValues(rq.Label).Inc()

	start := time.Now()
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, edgeURL+"/work?cost="+cost, nil)
	resp, err := client.Do(hreq)
	latency := time.Since(start).Seconds()

	code := "ERR"
	if resp != nil {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		code = strconv.Itoa(resp.StatusCode)
	}
	lg.OutcomeTotal.WithLabelValues(rq.Label, cost, code).Inc()
	lg.RequestSeconds.WithLabelValues(rq.Label, cost, code).Observe(latency)

	if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		atomic.AddInt64(&state.served, 1)
		lg.ServedTotal.WithLabelValues(rq.Label).Inc()
		return
	}
	atomic.AddInt64(&state.failed, 1)
}
