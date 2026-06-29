// backend is the single binary run as backend-1..4. Each instance:
//
//   - serves GET /work?cost=fast|slow with a configurable per-instance
//     base latency; the "slow" cost adds extra processing so request
//     costs are deliberately uneven (round-robin then skews, least-conn
//     rebalances)
//   - stamps its OWN measured processing time into the
//     X-Backend-Process-Ms response header so the edge can subtract it
//     and isolate pure edge overhead
//   - serves GET /healthz (shallow: process up) and GET /healthz?deep=1
//     (deep: also verifies the shared Postgres dependency) so the deep-
//     health-check cascading failure is reproducible
//   - exposes POST /admin/config to inject per-instance faults:
//     {"broken":true} (health checks fail + /work drops the connection,
//     so the edge sees a 502), {"extra_latency_ms":4000} (push past the
//     proxy timeout so the edge sees a 504)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hlsa2-labs/lab6-1/internal/metrics"
	"github.com/jackc/pgx/v5/pgxpool"
)

// backendConfig is the runtime fault state, flipped via /admin/config.
type backendConfig struct {
	Broken         bool `json:"broken"`
	ExtraLatencyMS int  `json:"extra_latency_ms"`
}

type configHolder struct {
	mu      sync.RWMutex
	current backendConfig
}

func (h *configHolder) snapshot() backendConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

func (h *configHolder) set(next backendConfig) {
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
	name := envOrDefault("BACKEND_NAME", "backend")
	port := envOrDefault("PORT", "8081")
	baseMS := envInt("BASE_LATENCY_MS", 10)
	jitterMS := envInt("JITTER_MS", 5)
	slowExtraMS := envInt("SLOW_EXTRA_MS", 120)
	dsn := os.Getenv("DATABASE_URL")

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	httpMetrics := metrics.NewHTTPMetrics(name)
	bm := metrics.NewBackend(name)
	holder := &configHolder{}

	// Lazy Postgres pool. pgxpool.New does not require the DB to be up
	// yet; deep health checks Ping() it on demand. A nil pool means
	// "no DSN configured" -> deep checks degrade to shallow.
	var pgPool *pgxpool.Pool
	if dsn != "" {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			log.Fatalf("bad DATABASE_URL: %v", err)
		}
		cfg.MaxConns = 5
		pgPool, err = pgxpool.NewWithConfig(context.Background(), cfg)
		if err != nil {
			log.Fatalf("pgxpool: %v", err)
		}
		defer pgPool.Close()
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Handle("/metrics", metrics.Handler())

	r.Get("/healthz", makeHealthHandler(holder, bm, pgPool))
	r.Get("/work", makeWorkHandler(name, baseMS, jitterMS, slowExtraMS, holder, bm, rng))

	r.Post("/admin/config", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Broken         *bool `json:"broken"`
			ExtraLatencyMS *int  `json:"extra_latency_ms"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next := holder.snapshot()
		if body.Broken != nil {
			next.Broken = *body.Broken
		}
		if body.ExtraLatencyMS != nil {
			next.ExtraLatencyMS = *body.ExtraLatencyMS
		}
		holder.set(next)
		if next.Broken {
			bm.Broken.Set(1)
		} else {
			bm.Broken.Set(0)
		}
		log.Printf("admin/config[%s]: broken=%t extra_latency_ms=%d", name, next.Broken, next.ExtraLatencyMS)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(next)
	})

	r.Get("/admin/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(holder.snapshot())
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("backend[%s] listening on :%s base=%dms jitter=%dms slow_extra=%dms deep_db=%t",
			name, port, baseMS, jitterMS, slowExtraMS, pgPool != nil)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func makeHealthHandler(holder *configHolder, bm *metrics.Backend, pgPool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deep := r.URL.Query().Has("deep") || r.URL.Query().Get("deep") == "1"
		depth := "shallow"
		if deep {
			depth = "deep"
		}
		cfg := holder.snapshot()

		// A broken instance fails BOTH depths: the process is up but
		// administratively faulted.
		if cfg.Broken {
			bm.HealthChecksTotal.WithLabelValues(depth, "fail").Inc()
			http.Error(w, "broken (injected fault)", http.StatusServiceUnavailable)
			return
		}

		// Deep check verifies the shared Postgres dependency. When
		// Postgres is briefly unavailable, this fails on EVERY backend
		// at once - the cascading failure the lab reproduces.
		if deep && pgPool != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
			defer cancel()
			if err := pgPool.Ping(ctx); err != nil {
				bm.HealthChecksTotal.WithLabelValues(depth, "fail").Inc()
				http.Error(w, "deep check failed: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
		}

		bm.HealthChecksTotal.WithLabelValues(depth, "ok").Inc()
		_, _ = w.Write([]byte("ok"))
	}
}

func makeWorkHandler(name string, baseMS, jitterMS, slowExtraMS int, holder *configHolder, bm *metrics.Backend, rng *rand.Rand) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		cfg := holder.snapshot()

		// Broken -> simulate "connection refused": hijack and slam the
		// connection shut so the edge's upstream call errors out and the
		// edge classifies it as 502 (a connectivity failure).
		if cfg.Broken {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil && conn != nil {
					_ = conn.Close()
					return
				}
			}
			http.Error(w, "broken (injected fault)", http.StatusInternalServerError)
			return
		}

		cost := r.URL.Query().Get("cost")
		if cost != "slow" {
			cost = "fast"
		}

		// Per-instance base + uniform jitter, plus the slow-endpoint
		// surcharge and any admin-injected latency.
		sleepMS := baseMS
		if jitterMS > 0 {
			sleepMS += rng.Intn(jitterMS)
		}
		if cost == "slow" {
			sleepMS += slowExtraMS
		}
		sleepMS += cfg.ExtraLatencyMS
		time.Sleep(time.Duration(sleepMS) * time.Millisecond)

		// Stamp the self-measured processing time so the edge can
		// subtract it and isolate its own overhead.
		proc := time.Since(start)
		w.Header().Set("X-Backend-Process-Ms", strconv.FormatFloat(float64(proc.Microseconds())/1000.0, 'f', 3, 64))
		w.Header().Set("X-Backend-Id", name)
		w.Header().Set("Content-Type", "application/json")

		bm.ProcessSeconds.WithLabelValues("/work").Observe(proc.Seconds())

		_ = json.NewEncoder(w).Encode(map[string]any{
			"backend":    name,
			"cost":       cost,
			"process_ms": float64(proc.Microseconds()) / 1000.0,
			"served_at":  time.Now().UTC().Format(time.RFC3339Nano),
			"value":      fmt.Sprintf("%s:%d", name, rng.Intn(1000)),
		})
	}
}
