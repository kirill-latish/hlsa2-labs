// gateway is the checkout aggregator. Inbound: GET /checkout?user=...
// Outbound: parallel fan-out to all five deps, with each dep tagged
// critical or non-critical. Critical deps must succeed; non-critical
// deps can be served from LKG cache, omitted, or shed.
//
// The five resilience controls are env knobs (BULKHEAD,
// CIRCUIT_BREAKER, FALLBACK, RETRY_BUDGET, LOAD_SHED) and default to
// "off" so step 5 of the topic guide actually breaks.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/hlsa2-labs/lab3-3/internal/breaker"
	"github.com/hlsa2-labs/lab3-3/internal/bulkhead"
	"github.com/hlsa2-labs/lab3-3/internal/fallback"
	"github.com/hlsa2-labs/lab3-3/internal/metrics"
	"github.com/hlsa2-labs/lab3-3/internal/retry"
)

// depConfig captures everything we need to call one dep.
type depConfig struct {
	Name     string
	URL      string
	Critical bool
}

// runtimeFlags carries the five toggles. Plain value type so it's
// safe to pass by copy into fanout goroutines.
type runtimeFlags struct {
	Bulkhead       bool `json:"bulkhead"`
	CircuitBreaker bool `json:"circuit_breaker"`
	Fallback       bool `json:"fallback"`
	RetryBudget    bool `json:"retry_budget"`
	LoadShed       bool `json:"load_shed"`
}

// flagsHolder lets /admin/config flip controls between bench runs
// without restarting the gateway. Each /checkout takes one snapshot
// at the top of the handler and uses it consistently for fanout.
type flagsHolder struct {
	mu      sync.RWMutex
	current runtimeFlags
}

func (h *flagsHolder) snapshot() runtimeFlags {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

func (h *flagsHolder) set(next runtimeFlags) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.current = next
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	v = stringsToLower(v)
	return v == "1" || v == "true" || v == "on" || v == "yes"
}

func stringsToLower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}

func main() {
	port := envOrDefault("PORT", "8080")
	maxConnsPerHost := envInt("MAX_CONNS_PER_HOST", 32)
	maxInflight := envInt("MAX_INFLIGHT", 256)
	depTimeout := time.Duration(envInt("DEP_TIMEOUT_MS", 800)) * time.Millisecond

	holder := &flagsHolder{current: runtimeFlags{
		Bulkhead:       envBool("BULKHEAD", false),
		CircuitBreaker: envBool("CIRCUIT_BREAKER", false),
		Fallback:       envBool("FALLBACK", false),
		RetryBudget:    envBool("RETRY_BUDGET", false),
		LoadShed:       envBool("LOAD_SHED", false),
	}}

	deps := []depConfig{
		{Name: "price", URL: envOrDefault("PRICE_URL", "http://price:8091"), Critical: true},
		{Name: "cart", URL: envOrDefault("CART_URL", "http://cart:8092"), Critical: true},
		{Name: "recommendations", URL: envOrDefault("RECOMMENDATIONS_URL", "http://recommendations:8093"), Critical: false},
		{Name: "reviews", URL: envOrDefault("REVIEWS_URL", "http://reviews:8094"), Critical: false},
		{Name: "recently-viewed", URL: envOrDefault("RECENTLY_VIEWED_URL", "http://recently-viewed:8095"), Critical: false},
	}

	httpMetrics := metrics.NewHTTPMetrics("gateway")
	res := metrics.NewResilience()

	publishControlStatus := func(f runtimeFlags) {
		for _, k := range []struct {
			name string
			on   bool
		}{
			{"BULKHEAD", f.Bulkhead},
			{"CIRCUIT_BREAKER", f.CircuitBreaker},
			{"FALLBACK", f.Fallback},
			{"RETRY_BUDGET", f.RetryBudget},
			{"LOAD_SHED", f.LoadShed},
		} {
			val := 0.0
			if k.on {
				val = 1
			}
			res.ControlStatus.WithLabelValues(k.name).Set(val)
		}
	}
	publishControlStatus(holder.snapshot())

	// Always allocate BOTH a shared pool and one pool per dep so the
	// holder can flip BULKHEAD on/off at runtime without restart. The
	// LoadShed knob is also runtime-readable on bulkhead.Pool, so we
	// give all pools the same MaxConns and the same timeout up front.
	sharedPool := bulkhead.NewPool(bulkhead.Options{
		MaxConnsPerHost: maxConnsPerHost,
		Timeout:         depTimeout,
	})
	perDepPools := make(map[string]*bulkhead.Pool, len(deps))
	breakers := make(map[string]*breaker.Breaker, len(deps))
	for _, d := range deps {
		perDepPools[d.Name] = bulkhead.NewPool(bulkhead.Options{
			MaxConnsPerHost: maxConnsPerHost,
			Timeout:         depTimeout,
		})
		breakers[d.Name] = breaker.New(5, 2*time.Second)
		critLabel := strconv.FormatBool(d.Critical)
		res.PoolMax.WithLabelValues(d.Name, critLabel).Set(float64(perDepPools[d.Name].Capacity))
	}

	// LKG cache, only used if FALLBACK=on.
	cache := fallback.NewCache(10 * time.Second)

	// Global retry budget at 10% of inbound rate. Inbound rate isn't
	// known a-priori, so we size to a conservative 30 rps refill (=
	// 10% of 300 inbound) with a 1s burst. Students can override via
	// env if their box runs higher.
	retryRPS := envInt("RETRY_BUDGET_RPS", 30)
	retryBurst := envInt("RETRY_BUDGET_BURST", 30)
	retryBudget := retry.NewBudget(float64(retryRPS), float64(retryBurst))

	// Inbound load shed: in-flight semaphore.
	var inflight int32

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", metrics.Handler())

	pickPools := func(f runtimeFlags) map[string]*bulkhead.Pool {
		if f.Bulkhead {
			return perDepPools
		}
		out := make(map[string]*bulkhead.Pool, len(deps))
		for _, d := range deps {
			out[d.Name] = sharedPool
		}
		return out
	}

	r.Get("/checkout", func(w http.ResponseWriter, r *http.Request) {
		flags := holder.snapshot()
		if flags.LoadShed {
			cur := atomic.AddInt32(&inflight, 1)
			defer atomic.AddInt32(&inflight, -1)
			if int(cur) > maxInflight {
				res.SheddedTotal.WithLabelValues("inbound_429", "gateway").Inc()
				res.CriticalJourneyTotal.WithLabelValues("shed").Inc()
				w.Header().Set("Retry-After", "1")
				http.Error(w, "load shed", http.StatusTooManyRequests)
				return
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), depTimeout*2)
		defer cancel()

		results := fanout(ctx, deps, pickPools(flags), breakers, cache, retryBudget, flags, res)

		// Decide outcome.
		outcome := classify(results, deps)
		res.CriticalJourneyTotal.WithLabelValues(outcome).Inc()

		w.Header().Set("Content-Type", "application/json")
		switch outcome {
		case "failed":
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"outcome":   outcome,
			"widgets":   payloads(results, deps),
			"served_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
	})

	// /admin/config lets the bench scripts flip controls between
	// labelled runs without restarting the gateway. Body is JSON of
	// the runtimeFlags shape; missing fields keep current values.
	r.Post("/admin/config", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Bulkhead       *bool `json:"bulkhead"`
			CircuitBreaker *bool `json:"circuit_breaker"`
			Fallback       *bool `json:"fallback"`
			RetryBudget    *bool `json:"retry_budget"`
			LoadShed       *bool `json:"load_shed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next := holder.snapshot()
		if body.Bulkhead != nil {
			next.Bulkhead = *body.Bulkhead
		}
		if body.CircuitBreaker != nil {
			next.CircuitBreaker = *body.CircuitBreaker
		}
		if body.Fallback != nil {
			next.Fallback = *body.Fallback
		}
		if body.RetryBudget != nil {
			next.RetryBudget = *body.RetryBudget
		}
		if body.LoadShed != nil {
			next.LoadShed = *body.LoadShed
		}
		holder.set(next)
		publishControlStatus(next)
		log.Printf("admin/config: %+v", next)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(next)
	})

	r.Get("/admin/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(holder.snapshot())
	})

	// Periodically push breaker states + pool depths to Prometheus.
	// Reports the pool that's actually serving traffic (bulkhead vs
	// shared) so the dashboard reflects what students are seeing.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			active := pickPools(holder.snapshot())
			for _, d := range deps {
				st, _ := breakers[d.Name].Snapshot()
				critLabel := strconv.FormatBool(d.Critical)
				res.BreakerState.WithLabelValues(d.Name, critLabel).Set(float64(st))
				res.PoolInflight.WithLabelValues(d.Name, critLabel).Set(float64(active[d.Name].Inflight()))
			}
		}
	}()

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		f0 := holder.snapshot()
		log.Printf("gateway listening on :%s bulkhead=%t cb=%t fallback=%t retry_budget=%t load_shed=%t",
			port, f0.Bulkhead, f0.CircuitBreaker, f0.Fallback, f0.RetryBudget, f0.LoadShed)
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

// depResult captures one dep call's outcome.
type depResult struct {
	Dep      string
	Critical bool
	OK       bool
	Status   int
	Payload  []byte
	Error    string
	Outcome  string // success|error|breaker_open|timeout|shed|fallback_lkg|fallback_omit
	FromLKG  bool
	Stale    bool
	LatMS    int64
}

func fanout(ctx context.Context, deps []depConfig, pools map[string]*bulkhead.Pool, brks map[string]*breaker.Breaker, cache *fallback.Cache, budget *retry.Budget, flags runtimeFlags, res *metrics.Resilience) []depResult {
	results := make([]depResult, len(deps))
	var wg sync.WaitGroup
	for i, d := range deps {
		wg.Add(1)
		go func(idx int, d depConfig) {
			defer wg.Done()
			results[idx] = callDep(ctx, d, pools[d.Name], brks[d.Name], cache, budget, flags, res)
		}(i, d)
	}
	wg.Wait()
	return results
}

func callDep(ctx context.Context, d depConfig, pool *bulkhead.Pool, brk *breaker.Breaker, cache *fallback.Cache, budget *retry.Budget, flags runtimeFlags, res *metrics.Resilience) depResult {
	critLabel := strconv.FormatBool(d.Critical)

	start := time.Now()
	defer func() {
		res.DepCallSeconds.WithLabelValues(d.Name, critLabel).Observe(time.Since(start).Seconds())
	}()

	maxAttempts := 1
	if flags.RetryBudget {
		maxAttempts = 2
	}

	var lastErr error
	var status int
	var body []byte

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Decide whether to consume a retry token.
			if !flags.RetryBudget {
				res.RetriesTotal.WithLabelValues(d.Name, "disabled").Inc()
				break
			}
			if !budget.TryConsume() {
				res.RetriesTotal.WithLabelValues(d.Name, "denied_budget").Inc()
				break
			}
			res.RetriesTotal.WithLabelValues(d.Name, "consumed_budget").Inc()
			time.Sleep(retry.Backoff(attempt-1, 25*time.Millisecond, 250*time.Millisecond))
		}

		// Bulkhead pool: acquire a slot. Returns ErrPoolFull when
		// LOAD_SHED and the dep's pool is at capacity.
		release, err := pool.Acquire(ctx, flags.LoadShed)
		if err != nil {
			res.SheddedTotal.WithLabelValues("dep_503", d.Name).Inc()
			res.DepCallsTotal.WithLabelValues(d.Name, critLabel, "shed").Inc()
			return depResult{Dep: d.Name, Critical: d.Critical, OK: false, Outcome: "shed", Error: err.Error()}
		}

		// Wrap the actual HTTP call in the breaker if enabled.
		doCall := func() error {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.URL+"/widget", nil)
			resp, err := pool.Client.Do(req)
			if err != nil {
				lastErr = err
				return err
			}
			defer resp.Body.Close()
			status = resp.StatusCode
			body, err = io.ReadAll(resp.Body)
			if err != nil {
				lastErr = err
				return err
			}
			if resp.StatusCode >= 500 {
				lastErr = fmt.Errorf("dep %s returned %d", d.Name, resp.StatusCode)
				return lastErr
			}
			return nil
		}

		var callErr error
		if flags.CircuitBreaker {
			callErr = brk.Do(doCall)
		} else {
			callErr = doCall()
		}
		release()

		if callErr == nil {
			latMS := time.Since(start).Milliseconds()
			res.DepCallsTotal.WithLabelValues(d.Name, critLabel, "success").Inc()
			cache.Put(d.Name, body)
			return depResult{
				Dep: d.Name, Critical: d.Critical,
				OK: true, Status: status, Payload: body, Outcome: "success",
				LatMS: latMS,
			}
		}

		if errors.Is(callErr, breaker.ErrOpen) {
			res.DepCallsTotal.WithLabelValues(d.Name, critLabel, "breaker_open").Inc()
			lastErr = callErr
			break
		}
		if errors.Is(callErr, context.DeadlineExceeded) {
			res.DepCallsTotal.WithLabelValues(d.Name, critLabel, "timeout").Inc()
			lastErr = callErr
			// Allow a retry if budget permits.
			continue
		}
		res.DepCallsTotal.WithLabelValues(d.Name, critLabel, "error").Inc()
	}

	// We failed all attempts. Fallback?
	if flags.Fallback && !d.Critical {
		if e, stale, ok := cache.Get(d.Name); ok {
			res.FallbacksServedTotal.WithLabelValues(d.Name, "lkg").Inc()
			return depResult{
				Dep: d.Name, Critical: false,
				OK: true, Outcome: "fallback_lkg", FromLKG: true, Stale: stale,
				Payload: e.Payload,
			}
		}
		// No LKG yet - omit the widget entirely. From the user's
		// perspective the page just doesn't show that section.
		res.FallbacksServedTotal.WithLabelValues(d.Name, "omit").Inc()
		return depResult{
			Dep: d.Name, Critical: false,
			OK: true, Outcome: "fallback_omit",
		}
	}

	errMsg := ""
	if lastErr != nil {
		errMsg = lastErr.Error()
	}
	return depResult{
		Dep: d.Name, Critical: d.Critical,
		OK: false, Status: status, Error: errMsg, Outcome: "error",
	}
}

// classify reduces per-dep results to one journey-level outcome.
//
//	success_full     - every dep ok (including non-critical)
//	success_degraded - all critical deps ok, at least one non-critical
//	                   served from LKG or omitted
//	failed           - any critical dep failed
func classify(results []depResult, deps []depConfig) string {
	allCriticalOK := true
	anyNonCriticalDegraded := false
	for _, r := range results {
		if r.Critical && !r.OK {
			allCriticalOK = false
		}
		if !r.Critical && (r.Outcome == "fallback_lkg" || r.Outcome == "fallback_omit" || !r.OK) {
			anyNonCriticalDegraded = true
		}
	}
	_ = deps
	if !allCriticalOK {
		return "failed"
	}
	if anyNonCriticalDegraded {
		return "success_degraded"
	}
	return "success_full"
}

// payloads composes the user-facing response. The exact shape doesn't
// matter for the lab; it only matters that we faithfully report what
// the loadgen sees (for analyzers).
func payloads(results []depResult, _ []depConfig) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		entry := map[string]any{
			"dep":     r.Dep,
			"outcome": r.Outcome,
		}
		if r.FromLKG {
			entry["stale"] = r.Stale
		}
		if len(r.Payload) > 0 {
			entry["bytes"] = len(r.Payload)
		}
		out = append(out, entry)
	}
	return out
}
