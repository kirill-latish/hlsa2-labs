// fault-injector holds the authoritative map of "dep -> current
// fault spec". Deps poll GET /faults/{dep} (200ms cache client-side).
// Operators flip faults via:
//
//	POST   /faults/{dep}   {"mode":"down|latency|errors","p99_ms":400,"error_rate":0.1}
//	DELETE /faults/{dep}
//	GET    /faults         (dashboard)
//	GET    /faults/{dep}
//
// Current state is also exported to Prometheus so a Grafana annotation
// can show "fault X was live from t1 to t2".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hlsa2-labs/lab3-3/internal/fault"
	"github.com/hlsa2-labs/lab3-3/internal/metrics"
)

type store struct {
	mu     sync.RWMutex
	faults map[string]fault.Spec
}

func newStore() *store {
	return &store{faults: make(map[string]fault.Spec, 8)}
}

func (s *store) get(dep string) (fault.Spec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	spec, ok := s.faults[dep]
	return spec, ok
}

func (s *store) set(dep string, spec fault.Spec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults[dep] = spec
}

func (s *store) delete(dep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.faults, dep)
}

func (s *store) snapshot() map[string]fault.Spec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]fault.Spec, len(s.faults))
	for k, v := range s.faults {
		out[k] = v
	}
	return out
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9000"
	}

	httpMetrics := metrics.NewHTTPMetrics("fault-injector")
	fim := metrics.NewFaultInjector()
	s := newStore()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", metrics.Handler())

	r.Get("/faults", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.snapshot())
	})

	r.Get("/faults/{dep}", func(w http.ResponseWriter, r *http.Request) {
		dep := chi.URLParam(r, "dep")
		spec, ok := s.get(dep)
		if !ok {
			// No fault is the steady state - return 404 so the client
			// caches "no fault" without ambiguity.
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(spec)
	})

	r.Post("/faults/{dep}", func(w http.ResponseWriter, r *http.Request) {
		dep := chi.URLParam(r, "dep")
		var spec fault.Spec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := validate(spec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.set(dep, spec)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"dep": dep, "applied": spec,
		})
		log.Printf("fault APPLY dep=%s mode=%s p99_ms=%d error_rate=%.3f",
			dep, spec.Mode, spec.P99MS, spec.ErrorRate)
	})

	r.Delete("/faults/{dep}", func(w http.ResponseWriter, r *http.Request) {
		dep := chi.URLParam(r, "dep")
		s.delete(dep)
		w.WriteHeader(http.StatusNoContent)
		log.Printf("fault CLEAR dep=%s", dep)
	})

	// Push current fault state to Prometheus every 5s so the dashboard
	// can annotate runs. We zero out modes that fell off.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			snap := s.snapshot()
			fim.FaultActive.Reset()
			for dep, spec := range snap {
				mode := spec.Mode
				if mode == fault.ModeNone {
					continue
				}
				fim.FaultActive.WithLabelValues(dep, mode).Set(1)
			}
		}
	}()

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("fault-injector listening on :%s", port)
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

func validate(s fault.Spec) error {
	switch s.Mode {
	case fault.ModeDown, fault.ModeLatency, fault.ModeErrors, fault.ModeNone:
	default:
		return errors.New("mode must be down|latency|errors (or empty to clear)")
	}
	if s.ErrorRate < 0 || s.ErrorRate > 1 {
		return errors.New("error_rate must be in [0,1]")
	}
	if s.P99MS < 0 {
		return errors.New("p99_ms must be >= 0")
	}
	return nil
}
