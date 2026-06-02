// fault-injector is the central fault store. Services poll it for
// their current Spec; the bench scripts POST/DELETE to mutate it.
//
// API:
//   GET    /faults/{service}        -> 200 Spec | 404
//   PUT    /faults/{service}        -> body: Spec
//   DELETE /faults/{service}        -> 204
//   GET    /faults                  -> {"<service>": Spec, ...}
//   GET    /healthz                 -> "ok"
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
)

type spec struct {
	Mode      string  `json:"mode"`
	P99MS     int     `json:"p99_ms,omitempty"`
	ErrorRate float64 `json:"error_rate,omitempty"`
}

type store struct {
	mu sync.RWMutex
	m  map[string]spec
}

func (s *store) get(svc string) (spec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[svc]
	return v, ok
}

func (s *store) put(svc string, sp spec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[string]spec)
	}
	s.m[svc] = sp
}

func (s *store) del(svc string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, svc)
}

func (s *store) snapshot() map[string]spec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]spec, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}

func main() {
	port := envOrDefault("PORT", "9000")
	st := &store{m: make(map[string]spec)}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	r.Get("/faults", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, st.snapshot())
	})

	r.Get("/faults/{service}", func(w http.ResponseWriter, r *http.Request) {
		svc := chi.URLParam(r, "service")
		v, ok := st.get(svc)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})

	r.Put("/faults/{service}", func(w http.ResponseWriter, r *http.Request) {
		svc := chi.URLParam(r, "service")
		var sp spec
		if err := json.NewDecoder(r.Body).Decode(&sp); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st.put(svc, sp)
		log.Printf("fault-injector: PUT %s -> %+v", svc, sp)
		writeJSON(w, http.StatusOK, sp)
	})

	r.Delete("/faults/{service}", func(w http.ResponseWriter, r *http.Request) {
		svc := chi.URLParam(r, "service")
		st.del(svc)
		log.Printf("fault-injector: DELETE %s", svc)
		w.WriteHeader(http.StatusNoContent)
	})

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

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
