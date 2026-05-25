// dep-svc is reused for price/cart/recommendations/reviews/recently-
// viewed. Behaviour is identical; only the DEP_NAME label and base
// latency knobs differ. Each request:
//
//  1. consults the fault-injector (200ms in-memory cache via
//     internal/fault) for this dep's current fault spec
//  2. applies one of: down (drop the connection / 503), latency
//     (sleep extra), errors (return 5xx at the given rate)
//  3. otherwise sleeps BASE_LATENCY_MS + Uniform(0, JITTER_MS) and
//     returns a small JSON body
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
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hlsa2-labs/lab3-3/internal/fault"
	"github.com/hlsa2-labs/lab3-3/internal/metrics"
)

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
	dep := envOrDefault("DEP_NAME", "unknown")
	port := envOrDefault("PORT", "8081")
	baseMS := envInt("BASE_LATENCY_MS", 25)
	jitterMS := envInt("JITTER_MS", 15)
	injURL := envOrDefault("FAULT_INJECTOR_URL", "http://fault-injector:9000")

	rand.New(rand.NewSource(time.Now().UnixNano()))

	httpMetrics := metrics.NewHTTPMetrics(dep)
	faultClient := fault.New(injURL)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", metrics.Handler())

	r.Get("/widget", makeWidgetHandler(dep, baseMS, jitterMS, faultClient))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("dep-svc[%s] listening on :%s base=%dms jitter=%dms injector=%s",
			dep, port, baseMS, jitterMS, injURL)
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

// widget handler closure carries dep config so the route registration
// stays a one-liner.
func makeWidgetHandler(dep string, baseMS, jitterMS int, fc *fault.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		spec := fc.Get(dep)
		switch spec.Mode {
		case fault.ModeDown:
			// 503 simulates "service is down" - the gateway sees the
			// request fail, but the connection still closes cleanly.
			// The topic teaches that breakerless callers will keep
			// retrying for the full timeout: that is the failure we
			// want students to see in step 5.
			http.Error(w, "down (injected fault)", http.StatusServiceUnavailable)
			return
		case fault.ModeLatency:
			// extra latency stacks on the base latency so the
			// histogram peak shifts and you can see it in Grafana.
			extra := time.Duration(spec.P99MS) * time.Millisecond
			time.Sleep(extra)
		case fault.ModeErrors:
			if rand.Float64() < spec.ErrorRate {
				http.Error(w, "error (injected fault)", http.StatusInternalServerError)
				return
			}
		}

		// Base + jitter sleep.
		sleep := time.Duration(baseMS) * time.Millisecond
		if jitterMS > 0 {
			sleep += time.Duration(rand.Intn(jitterMS)) * time.Millisecond
		}
		time.Sleep(sleep)

		// Body is small so the gateway can fan out at sustained RPS.
		body := map[string]any{
			"dep":         dep,
			"served_at":   time.Now().UTC().Format(time.RFC3339Nano),
			"latency_ms":  sleep.Milliseconds(),
			"value":       fmt.Sprintf("%s:%d", dep, rand.Intn(1000)),
			"injected":    spec.Mode != fault.ModeNone,
			"injected_as": spec.Mode,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
}
