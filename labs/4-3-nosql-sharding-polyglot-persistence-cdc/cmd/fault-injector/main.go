// fault-injector is the central hot-entity store for lab 4-3. The
// loadgen polls it via internal/fault, and `make inject-hot` /
// `make clear-hot` mutate it.
//
// API:
//
//	GET    /faults/{slot}        -> 200 Spec | 404
//	PUT    /faults/{slot}        -> body: Spec
//	DELETE /faults/{slot}        -> 204
//	GET    /faults               -> {"<slot>": Spec, ...}
//	GET    /healthz              -> "ok"
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
	Mode   string  `json:"mode"`
	Entity string  `json:"entity,omitempty"`
	Weight float64 `json:"weight,omitempty"`
}

type store struct {
	mu sync.RWMutex
	m  map[string]spec
}

func (s *store) get(slot string) (spec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[slot]
	return v, ok
}

func (s *store) put(slot string, sp spec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[string]spec)
	}
	s.m[slot] = sp
}

func (s *store) del(slot string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, slot)
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

	r.Get("/faults/{slot}", func(w http.ResponseWriter, r *http.Request) {
		slot := chi.URLParam(r, "slot")
		v, ok := st.get(slot)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})

	r.Put("/faults/{slot}", func(w http.ResponseWriter, r *http.Request) {
		slot := chi.URLParam(r, "slot")
		var sp spec
		if err := json.NewDecoder(r.Body).Decode(&sp); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st.put(slot, sp)
		log.Printf("fault-injector: PUT %s -> %+v", slot, sp)
		writeJSON(w, http.StatusOK, sp)
	})

	r.Delete("/faults/{slot}", func(w http.ResponseWriter, r *http.Request) {
		slot := chi.URLParam(r, "slot")
		st.del(slot)
		log.Printf("fault-injector: DELETE %s", slot)
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
