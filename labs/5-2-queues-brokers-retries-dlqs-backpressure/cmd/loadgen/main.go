// loadgen is the in-cluster controller that drives the producer rate
// and toggles fault injection. The bench/inject scripts talk to loadgen
// (so `make ps` shows a distinct load driver); loadgen translates each
// run into producer /admin/config + producer /start calls and polls the
// producer to completion.
//
// HTTP API:
//
//	POST   /start  {"rate":300,"duration_s":300,"label":"baseline",
//	                "poison_count":0,"transient_rate":0,"permanent_rate":0,
//	                "overload_multiplier":1,"backpressure":false}
//	POST   /stop
//	GET    /state
//	GET    /summary
//	GET    /healthz / /metrics
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/hlsa2-labs/lab5-2/internal/metrics"
)

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type startReq struct {
	Rate               int     `json:"rate"`
	DurationS          int     `json:"duration_s"`
	Label              string  `json:"label"`
	PoisonCount        int     `json:"poison_count"`
	TransientRate      float64 `json:"transient_rate"`
	PermanentRate      float64 `json:"permanent_rate"`
	OverloadMultiplier float64 `json:"overload_multiplier"`
	Backpressure       bool    `json:"backpressure"`
}

type state struct {
	mu          sync.Mutex
	running     bool
	label       string
	startedAt   time.Time
	lastSummary map[string]any
}

func (s *state) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{
		"running":    s.running,
		"label":      s.label,
		"started_at": s.startedAt.UTC().Format(time.RFC3339Nano),
	}
	for k, v := range s.lastSummary {
		out[k] = v
	}
	return out
}

func main() {
	port := envOrDefault("PORT", "8090")
	producerURL := envOrDefault("PRODUCER_URL", "http://producer:8080")

	httpMetrics := metrics.NewHTTPMetrics("loadgen")
	client := &http.Client{Timeout: 5 * time.Second}
	st := &state{}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", metrics.Handler())

	r.Get("/state", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, st.snapshot()) })
	r.Get("/summary", func(w http.ResponseWriter, _ *http.Request) {
		// Prefer a live producer summary while running, else the
		// snapshot captured at completion.
		if s, err := getJSON(client, producerURL+"/summary"); err == nil {
			writeJSON(w, s)
			return
		}
		writeJSON(w, st.snapshot())
	})

	r.Post("/stop", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = postJSON(client, producerURL+"/stop", nil)
		st.mu.Lock()
		st.running = false
		st.mu.Unlock()
		_, _ = w.Write([]byte("stopped"))
	})

	r.Post("/start", func(w http.ResponseWriter, req *http.Request) {
		var body startReq
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.Rate <= 0 {
			body.Rate = 240
		}
		if body.DurationS <= 0 {
			body.DurationS = 60
		}
		if body.Label == "" {
			body.Label = "run"
		}
		if body.OverloadMultiplier <= 0 {
			body.OverloadMultiplier = 1.0
		}

		// 1) Configure fault injection on the producer.
		cfg := map[string]any{
			"poison_count":        body.PoisonCount,
			"transient_rate":      body.TransientRate,
			"permanent_rate":      body.PermanentRate,
			"overload_multiplier": body.OverloadMultiplier,
			"backpressure":        body.Backpressure,
		}
		if _, err := postJSON(client, producerURL+"/admin/config", cfg); err != nil {
			http.Error(w, "configure producer: "+err.Error(), http.StatusBadGateway)
			return
		}
		// 2) Start the producer run.
		startBody := map[string]any{"rate": body.Rate, "duration_s": body.DurationS, "label": body.Label}
		if _, err := postJSON(client, producerURL+"/start", startBody); err != nil {
			http.Error(w, "start producer: "+err.Error(), http.StatusBadGateway)
			return
		}

		st.mu.Lock()
		st.running = true
		st.label = body.Label
		st.startedAt = time.Now()
		st.lastSummary = nil
		st.mu.Unlock()

		// 3) Poll the producer until it reports running=false, then
		// snapshot its summary so analyzers can read produced/errors.
		go func(maxWait int) {
			deadline := time.Now().Add(time.Duration(maxWait+30) * time.Second)
			for {
				time.Sleep(3 * time.Second)
				s, err := getJSON(client, producerURL+"/state")
				if err == nil {
					if running, _ := s["running"].(bool); !running {
						if sum, e := getJSON(client, producerURL+"/summary"); e == nil {
							st.mu.Lock()
							st.lastSummary = sum
							st.running = false
							st.mu.Unlock()
						}
						return
					}
				}
				if time.Now().After(deadline) {
					_, _ = postJSON(client, producerURL+"/stop", nil)
					st.mu.Lock()
					st.running = false
					st.mu.Unlock()
					return
				}
			}
		}(body.DurationS)

		writeJSON(w, st.snapshot())
	})

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("loadgen listening on :%s producer=%s", port, producerURL)
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

func getJSON(client *http.Client, url string) (map[string]any, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func postJSON(client *http.Client, url string, body any) (map[string]any, error) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, errors.New(resp.Status + ": " + string(b))
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
