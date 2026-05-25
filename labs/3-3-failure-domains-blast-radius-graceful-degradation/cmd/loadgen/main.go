// loadgen is an in-cluster Go HTTP service that drives the gateway at
// a constant arrival rate (baseline profile) or an arrival-rate ramp
// past saturation with in-loadgen retries (overload profile).
//
// Why in-cluster Go and not k6? Because step 7 specifically wants
// in-loadgen retries (to provoke a storm before LOAD_SHED kicks in)
// and step 6 wants identical-fault before/after, and a single HTTP
// service that exports Prometheus metrics is the simplest way to keep
// both runs comparable.
//
// HTTP API:
//
//	POST   /start  {"rate_rps": 200, "duration_s": 60, "label": "baseline", "profile": "baseline"|"overload"}
//	POST   /stop
//	GET    /state
//	GET    /summary?label=baseline  -> aggregated counts since label started
//	GET    /healthz                 -> liveness
//	GET    /metrics                 -> Prometheus
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
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
	"github.com/hlsa2-labs/lab3-3/internal/metrics"
)

type startReq struct {
	RateRPS        int    `json:"rate_rps"`
	DurationS      int    `json:"duration_s"`
	Label          string `json:"label"`
	Profile        string `json:"profile"`
	InLoadgenRetry int    `json:"in_loadgen_retries"`
}

type runState struct {
	mu             sync.Mutex
	running        bool
	cancel         context.CancelFunc
	label          string
	profile        string
	startedAt      time.Time
	endsAt         time.Time
	rateRPS        int
	durationS      int
	inFlightLimit  int
	inLoadgenRetry int

	// per-run counters (also exported to Prometheus, but kept here so
	// /summary?label=... can return a snapshot atomically).
	offered  int64
	served   int64
	retries  int64
	failed   int64
}

func (s *runState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"running":           s.running,
		"label":             s.label,
		"profile":           s.profile,
		"started_at":        s.startedAt.UTC().Format(time.RFC3339Nano),
		"ends_at":           s.endsAt.UTC().Format(time.RFC3339Nano),
		"rate_rps":          s.rateRPS,
		"duration_s":        s.durationS,
		"in_loadgen_retry":  s.inLoadgenRetry,
		"offered":           atomic.LoadInt64(&s.offered),
		"served":            atomic.LoadInt64(&s.served),
		"retries":           atomic.LoadInt64(&s.retries),
		"failed":            atomic.LoadInt64(&s.failed),
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
	gatewayURL := envOrDefault("GATEWAY_URL", "http://gateway:8080")
	httpMetrics := metrics.NewHTTPMetrics("loadgen")
	lg := metrics.NewLoadgen()

	state := &runState{}

	// One transport with generous limits so loadgen itself isn't the
	// bottleneck during overload runs.
	client := &http.Client{
		Timeout: 3 * time.Second,
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

	r.Get("/summary", func(w http.ResponseWriter, r *http.Request) {
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

	r.Post("/start", func(w http.ResponseWriter, r *http.Request) {
		var req startReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Profile == "" {
			req.Profile = "baseline"
		}
		if req.Label == "" {
			req.Label = req.Profile
		}
		if req.RateRPS <= 0 {
			req.RateRPS = 200
		}
		if req.DurationS <= 0 {
			req.DurationS = 60
		}

		state.mu.Lock()
		if state.running {
			state.cancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		state.running = true
		state.cancel = cancel
		state.label = req.Label
		state.profile = req.Profile
		state.startedAt = time.Now()
		state.endsAt = time.Now().Add(time.Duration(req.DurationS) * time.Second)
		state.rateRPS = req.RateRPS
		state.durationS = req.DurationS
		state.inFlightLimit = 1024
		state.inLoadgenRetry = req.InLoadgenRetry
		atomic.StoreInt64(&state.offered, 0)
		atomic.StoreInt64(&state.served, 0)
		atomic.StoreInt64(&state.retries, 0)
		atomic.StoreInt64(&state.failed, 0)
		state.mu.Unlock()

		lg.RateRPS.WithLabelValues(req.Label, req.Profile).Set(float64(req.RateRPS))

		go drive(ctx, req, gatewayURL, client, lg, state)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state.snapshot())
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("loadgen listening on :%s, target=%s", port, gatewayURL)
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

// drive runs the actual workload until ctx is cancelled or duration
// elapses. Arrival rate is enforced by a ticker so the offered rate
// matches the configured RPS regardless of how the gateway responds.
//
// For profile=overload we still use a ticker, but the caller is
// expected to fire `make bench-overload` against increasing rates;
// in-loadgen retries (req.InLoadgenRetry) amplify pressure when the
// gateway starts to fail. This is exactly the loop the topic guide's
// step 7 talks about.
func drive(ctx context.Context, req startReq, gatewayURL string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	tick := time.Duration(float64(time.Second) / float64(req.RateRPS))
	if tick <= 0 {
		tick = time.Millisecond
	}
	deadline := time.Now().Add(time.Duration(req.DurationS) * time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()

	var wg sync.WaitGroup
	limit := make(chan struct{}, 2048) // very loose; we want offered to match config

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			state.mu.Lock()
			state.running = false
			state.mu.Unlock()
			return
		case now := <-t.C:
			if now.After(deadline) {
				wg.Wait()
				state.mu.Lock()
				state.running = false
				state.mu.Unlock()
				return
			}
			limit <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-limit }()
				do(ctx, req, gatewayURL, client, lg, state)
			}()
		}
	}
}

func do(ctx context.Context, req startReq, gatewayURL string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	atomic.AddInt64(&state.offered, 1)
	lg.OfferedTotal.WithLabelValues(req.Label, req.Profile).Inc()

	tries := 1 + req.InLoadgenRetry
	for i := 0; i < tries; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		start := time.Now()
		hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/checkout?user=loadgen", nil)
		resp, err := client.Do(hreq)
		latency := time.Since(start).Seconds()

		code := "ERR"
		if resp != nil {
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			code = strconv.Itoa(resp.StatusCode)
		}
		lg.OutcomeTotal.WithLabelValues(req.Label, req.Profile, code).Inc()
		lg.RequestSeconds.WithLabelValues(req.Label, req.Profile, code).Observe(latency)

		if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			atomic.AddInt64(&state.served, 1)
			lg.ServedTotal.WithLabelValues(req.Label, req.Profile).Inc()
			return
		}
		// Retry if loadgen-side retries are configured (overload profile).
		if i+1 < tries {
			atomic.AddInt64(&state.retries, 1)
			lg.RetriesTotal.WithLabelValues(req.Label, req.Profile).Inc()
			// Tight backoff - we WANT amplification here.
			time.Sleep(50 * time.Millisecond)
			continue
		}
		atomic.AddInt64(&state.failed, 1)
		_ = err
		return
	}
}
