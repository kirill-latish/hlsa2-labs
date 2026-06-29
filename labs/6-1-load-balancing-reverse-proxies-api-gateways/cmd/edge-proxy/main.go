// edge-proxy is an INSTRUMENTED Go reverse proxy in front of four
// backends. It is deliberately written in Go (not NGINX/Envoy/HAProxy)
// so that every quantity the topic guide asks for emits a Prometheus
// metric: the edge-overhead span timing, the balancing-algorithm
// distribution, the health-check-depth knob, and 5xx classification.
// See README.md ("Why a Go reverse proxy") for the documented option.
//
// Responsibilities:
//   - reverse-proxy GET traffic to backend-1..4 by round-robin or
//     least-conn (runtime-switchable via POST /admin/config)
//   - run an active health-check loop (shallow or deep) that marks
//     backends up/down after `failure_threshold` consecutive failures
//   - measure EDGE OVERHEAD separately from backend processing time,
//     using the backend's X-Backend-Process-Ms response header
//   - classify failures: 502 (cannot connect), 503 (no healthy
//     backends), 504 (backend exceeded the proxy timeout)
//   - admin: fail/restore a backend (forwarded to the backend so the
//     proxy still has to *detect* the failure via health checks)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hlsa2-labs/lab6-1/internal/metrics"
	"github.com/hlsa2-labs/lab6-1/internal/proxy"
)

// edgeConfig is the runtime-tunable config flipped via /admin/config.
type edgeConfig struct {
	Algo             string `json:"algo"`               // round-robin|least-conn
	HealthIntervalMS int    `json:"health_interval_ms"` // active health-check period
	FailureThreshold int    `json:"failure_threshold"`  // consecutive fails -> down
	HealthDepth      string `json:"health_depth"`       // shallow|deep
}

type configHolder struct {
	mu      sync.RWMutex
	current edgeConfig
}

func (h *configHolder) snapshot() edgeConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

func (h *configHolder) set(next edgeConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.current = next
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
	proxyTimeout := time.Duration(envInt("PROXY_TIMEOUT_MS", 2000)) * time.Millisecond

	// Backend roster. Each backend listens on the same in-container
	// port; only the hostname differs.
	backends := []*proxy.Backend{
		{ID: "backend-1", URL: envOrDefault("BACKEND_1_URL", "http://backend-1:8081")},
		{ID: "backend-2", URL: envOrDefault("BACKEND_2_URL", "http://backend-2:8081")},
		{ID: "backend-3", URL: envOrDefault("BACKEND_3_URL", "http://backend-3:8081")},
		{ID: "backend-4", URL: envOrDefault("BACKEND_4_URL", "http://backend-4:8081")},
	}
	// Start optimistic so the stack serves immediately once backends
	// are up (compose gates the edge on backend health anyway).
	for _, b := range backends {
		b.SetHealthy(true)
	}
	pool := proxy.NewPool(backends)

	holder := &configHolder{current: edgeConfig{
		Algo:             envOrDefault("ALGO", "round-robin"),
		HealthIntervalMS: envInt("HEALTH_INTERVAL_MS", 10000),
		FailureThreshold: envInt("FAILURE_THRESHOLD", 3),
		HealthDepth:      envOrDefault("HEALTH_DEPTH", "shallow"),
	}}

	httpMetrics := metrics.NewHTTPMetrics("edge-proxy")
	edge := metrics.NewEdge()

	publishConfig := func(c edgeConfig) {
		edge.AlgoActive.Reset()
		edge.AlgoActive.WithLabelValues(c.Algo).Set(1)
		edge.DepthActive.Reset()
		edge.DepthActive.WithLabelValues(c.HealthDepth).Set(1)
		edge.ConfigValue.WithLabelValues("health_interval_ms").Set(float64(c.HealthIntervalMS))
		edge.ConfigValue.WithLabelValues("failure_threshold").Set(float64(c.FailureThreshold))
	}
	publishConfig(holder.snapshot())

	// Upstream client: per-call timeout drives the 504 classification.
	// Generous pool so the proxy itself is never the bottleneck.
	client := &http.Client{
		Timeout: proxyTimeout,
		Transport: &http.Transport{
			MaxConnsPerHost:     1024,
			MaxIdleConnsPerHost: 256,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	// A separate, short-timeout client for active health checks so a
	// hung backend can't stall the whole health loop.
	healthClient := &http.Client{Timeout: 2 * time.Second}
	// Admin-forward client (fail/restore propagation to backends).
	adminClient := &http.Client{Timeout: 2 * time.Second}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", metrics.Handler())

	proxyHandler := makeProxyHandler(pool, holder, client, proxyTimeout, edge)
	r.Handle("/work", proxyHandler)
	r.NotFound(proxyHandler)

	// --- Admin API -----------------------------------------------------

	r.Post("/admin/config", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Algo             *string `json:"algo"`
			HealthIntervalMS *int    `json:"health_interval_ms"`
			FailureThreshold *int    `json:"failure_threshold"`
			HealthDepth      *string `json:"health_depth"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next := holder.snapshot()
		if body.Algo != nil {
			if *body.Algo != "round-robin" && *body.Algo != "least-conn" {
				http.Error(w, "algo must be round-robin|least-conn", http.StatusBadRequest)
				return
			}
			next.Algo = *body.Algo
		}
		if body.HealthIntervalMS != nil && *body.HealthIntervalMS > 0 {
			next.HealthIntervalMS = *body.HealthIntervalMS
		}
		if body.FailureThreshold != nil && *body.FailureThreshold > 0 {
			next.FailureThreshold = *body.FailureThreshold
		}
		if body.HealthDepth != nil {
			if *body.HealthDepth != "shallow" && *body.HealthDepth != "deep" {
				http.Error(w, "health_depth must be shallow|deep", http.StatusBadRequest)
				return
			}
			next.HealthDepth = *body.HealthDepth
		}
		holder.set(next)
		publishConfig(next)
		log.Printf("admin/config: %+v", next)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(next)
	})

	r.Get("/admin/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(holder.snapshot())
	})

	r.Get("/admin/status", func(w http.ResponseWriter, _ *http.Request) {
		cfg := holder.snapshot()
		type backendView struct {
			ID               string `json:"id"`
			URL              string `json:"url"`
			Healthy          bool   `json:"healthy"`
			Inflight         int64  `json:"inflight"`
			Requests         int64  `json:"requests"`
			ConsecutiveFails int    `json:"consecutive_fails"`
		}
		views := make([]backendView, 0, len(pool.Backends()))
		healthyCount := 0
		for _, b := range pool.Backends() {
			if b.Healthy() {
				healthyCount++
			}
			views = append(views, backendView{
				ID: b.ID, URL: b.URL, Healthy: b.Healthy(),
				Inflight: b.Inflight(), Requests: b.Requests(),
				ConsecutiveFails: b.ConsecutiveFails(),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"algo":             cfg.Algo,
			"health_depth":     cfg.HealthDepth,
			"interval_ms":      cfg.HealthIntervalMS,
			"failure_threshold": cfg.FailureThreshold,
			"proxy_timeout_ms": proxyTimeout.Milliseconds(),
			"healthy_backends": healthyCount,
			"backends":         views,
		})
	})

	// fail/restore are forwarded to the backend so the proxy still has
	// to *detect* the change through its health-check loop - exactly
	// the failover-detection-time the topic guide measures.
	r.Post("/admin/backend/{id}/fail", func(w http.ResponseWriter, r *http.Request) {
		forwardBackendFault(w, r, pool, adminClient, true)
	})
	r.Post("/admin/backend/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		forwardBackendFault(w, r, pool, adminClient, false)
	})

	// --- Active health-check loop --------------------------------------
	go healthLoop(pool, holder, healthClient, edge)

	// --- Gauge publisher ----------------------------------------------
	go func() {
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()
		for range t.C {
			for _, b := range pool.Backends() {
				up := 0.0
				if b.Healthy() {
					up = 1
				}
				edge.BackendUp.WithLabelValues(b.ID).Set(up)
				edge.BackendInflight.WithLabelValues(b.ID).Set(float64(b.Inflight()))
			}
		}
	}()

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		c := holder.snapshot()
		log.Printf("edge-proxy listening on :%s algo=%s depth=%s interval=%dms threshold=%d timeout=%s",
			port, c.Algo, c.HealthDepth, c.HealthIntervalMS, c.FailureThreshold, proxyTimeout)
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

// makeProxyHandler returns the reverse-proxy handler. It is the heart
// of the lab: pick a backend, proxy the call, separate edge overhead
// from backend processing, and classify failures into 502/503/504.
func makeProxyHandler(pool *proxy.Pool, holder *configHolder, client *http.Client, proxyTimeout time.Duration, edge *metrics.Edge) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqStart := time.Now()
		cfg := holder.snapshot()

		b := pool.Pick(cfg.Algo)
		if b == nil {
			// No healthy backends -> capacity/health problem.
			edge.FiveXXTotal.WithLabelValues("503").Inc()
			http.Error(w, "no healthy backends", http.StatusServiceUnavailable)
			return
		}

		b.Acquire()
		defer b.Release()
		b.CountRequest()
		edge.BackendRequestsTotal.WithLabelValues(b.ID).Inc()

		ctx, cancel := context.WithTimeout(r.Context(), proxyTimeout)
		defer cancel()

		upURL := b.URL + r.URL.RequestURI()
		upReq, err := http.NewRequestWithContext(ctx, r.Method, upURL, nil)
		if err != nil {
			edge.FiveXXTotal.WithLabelValues("502").Inc()
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}

		resp, err := client.Do(upReq)
		if err != nil {
			// Timeout -> 504 (backend reached but too slow). Anything
			// else (dial refused, connection reset, EOF) -> 502.
			if isTimeout(err) {
				edge.FiveXXTotal.WithLabelValues("504").Inc()
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			} else {
				edge.FiveXXTotal.WithLabelValues("502").Inc()
				http.Error(w, "bad gateway", http.StatusBadGateway)
			}
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		// Backend's self-measured processing time, used to subtract out
		// backend latency and leave the pure edge overhead.
		backendSec := parseProcessMs(resp.Header.Get("X-Backend-Process-Ms")) / 1000.0

		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)

		total := time.Since(reqStart).Seconds()
		overhead := total - backendSec
		if overhead < 0 {
			overhead = 0
		}
		edge.OverheadSeconds.Observe(overhead)
		edge.RequestSeconds.WithLabelValues(strconv.Itoa(resp.StatusCode)).Observe(total)
		if resp.StatusCode >= 500 {
			edge.FiveXXTotal.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()
		}
	}
}

func healthLoop(pool *proxy.Pool, holder *configHolder, client *http.Client, edge *metrics.Edge) {
	for {
		cfg := holder.snapshot()
		depth := cfg.HealthDepth
		for _, b := range pool.Backends() {
			ok := probe(client, b.URL, depth)
			if !ok {
				edge.HealthCheckFailTotal.WithLabelValues(b.ID, depth).Inc()
			}
			b.MarkProbe(ok, cfg.FailureThreshold)
		}
		interval := time.Duration(cfg.HealthIntervalMS) * time.Millisecond
		if interval <= 0 {
			interval = time.Second
		}
		time.Sleep(interval)
	}
}

// probe does one health check. shallow hits /healthz (process up);
// deep hits /healthz?deep=1 (backend then verifies Postgres).
func probe(client *http.Client, base, depth string) bool {
	url := base + "/healthz"
	if depth == "deep" {
		url += "?deep=1"
	}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func forwardBackendFault(w http.ResponseWriter, r *http.Request, pool *proxy.Pool, client *http.Client, broken bool) {
	id := chi.URLParam(r, "id")
	b := pool.ByID(id)
	if b == nil {
		http.Error(w, "unknown backend: "+id, http.StatusNotFound)
		return
	}
	payload, _ := json.Marshal(map[string]any{"broken": broken})
	req, _ := http.NewRequest(http.MethodPost, b.URL+"/admin/config", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to reach backend admin: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	action := "restore"
	if broken {
		action = "fail"
	}
	log.Printf("admin/backend %s -> %s (forwarded broken=%t)", id, action, broken)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"backend": id, "action": action, "broken": broken})
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		// Skip hop-by-hop-ish noise; keep it simple for the lab.
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func parseProcessMs(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	// http.Client.Timeout surfaces as an *url.Error wrapping a string
	// "context deadline exceeded (Client.Timeout exceeded ...)".
	return strings.Contains(err.Error(), "Client.Timeout") ||
		strings.Contains(err.Error(), "context deadline exceeded")
}
