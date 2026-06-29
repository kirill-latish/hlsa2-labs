// loadgen is an in-cluster Go HTTP service that drives the app's cache
// endpoints. A single service that also exports Prometheus metrics is
// the simplest way to keep labelled runs comparable across the four
// experiments the topic guide asks for:
//
//	baseline   - Zipfian (or uniform) reads across the keyspace
//	stampede   - hammer ONE hot key at a high fixed RPS
//	hotkey     - send a WEIGHT fraction of traffic to one key
//	staleness  - writers update the SoR while readers sample through the
//	             cache; each read is compared to /source to count stale
//	             samples and the max staleness duration
//
// HTTP API:
//
//	POST /start   {"mode":"baseline","dist":"zipf","rate_rps":2000,"duration_s":300,"label":"baseline"}
//	POST /stop
//	GET  /state
//	GET  /summary
//	GET  /healthz
//	GET  /metrics
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/hlsa2-labs/lab5-1/internal/metrics"
)

const (
	ModeBaseline  = "baseline"
	ModeStampede  = "stampede"
	ModeHotKey    = "hotkey"
	ModeStaleness = "staleness"
)

type startReq struct {
	Mode         string  `json:"mode"`
	Dist         string  `json:"dist"`
	RateRPS      int     `json:"rate_rps"`
	DurationS    int     `json:"duration_s"`
	Label        string  `json:"label"`
	HotKey       string  `json:"hot_key"`
	HotWeight    float64 `json:"hot_weight"`
	KeyspaceSize int     `json:"keyspace_size"`
	Writers      int     `json:"writers"`
	Strategy     string  `json:"strategy"`
}

type runState struct {
	mu        sync.Mutex
	running   bool
	cancel    context.CancelFunc
	req       startReq
	startedAt time.Time
	endsAt    time.Time

	offered int64
	served  int64
	failed  int64

	// staleness race counters.
	fresh     int64
	stale     int64
	maxStaleN int64 // max staleness in nanoseconds
}

func (s *runState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	fresh := atomic.LoadInt64(&s.fresh)
	stale := atomic.LoadInt64(&s.stale)
	samples := fresh + stale
	fracStale := 0.0
	if samples > 0 {
		fracStale = float64(stale) / float64(samples)
	}
	return map[string]any{
		"running":               s.running,
		"label":                 s.req.Label,
		"mode":                  s.req.Mode,
		"dist":                  s.req.Dist,
		"strategy":              s.req.Strategy,
		"hot_key":               s.req.HotKey,
		"hot_weight":            s.req.HotWeight,
		"rate_rps":              s.req.RateRPS,
		"duration_s":            s.req.DurationS,
		"started_at":            s.startedAt.UTC().Format(time.RFC3339Nano),
		"ends_at":               s.endsAt.UTC().Format(time.RFC3339Nano),
		"offered":               atomic.LoadInt64(&s.offered),
		"served":                atomic.LoadInt64(&s.served),
		"failed":                atomic.LoadInt64(&s.failed),
		"fresh_samples":         fresh,
		"stale_samples":         stale,
		"fraction_stale":        fracStale,
		"max_staleness_seconds": float64(atomic.LoadInt64(&s.maxStaleN)) / 1e9,
	}
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
	port := envOrDefault("PORT", "8090")
	appURL := envOrDefault("APP_URL", "http://app:8080")
	defaultKeyspace := envInt("KEYSPACE_SIZE", 10000)

	httpMetrics := metrics.NewHTTPMetrics("loadgen")
	lg := metrics.NewLoadgen()
	state := &runState{}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxConnsPerHost:     2048,
			MaxIdleConnsPerHost: 512,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", metrics.Handler())

	r.Get("/state", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, state.snapshot()) })
	r.Get("/summary", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, state.snapshot()) })

	r.Post("/stop", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		if state.cancel != nil {
			state.cancel()
		}
		state.running = false
		state.mu.Unlock()
		_, _ = w.Write([]byte("stopped"))
	})

	r.Post("/start", func(w http.ResponseWriter, r *http.Request) {
		var req startReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		applyDefaults(&req, defaultKeyspace)

		state.mu.Lock()
		if state.running && state.cancel != nil {
			state.cancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		state.running = true
		state.cancel = cancel
		state.req = req
		state.startedAt = time.Now()
		state.endsAt = time.Now().Add(time.Duration(req.DurationS) * time.Second)
		atomic.StoreInt64(&state.offered, 0)
		atomic.StoreInt64(&state.served, 0)
		atomic.StoreInt64(&state.failed, 0)
		atomic.StoreInt64(&state.fresh, 0)
		atomic.StoreInt64(&state.stale, 0)
		atomic.StoreInt64(&state.maxStaleN, 0)
		state.mu.Unlock()

		lg.RateRPS.WithLabelValues(req.Label, req.Mode).Set(float64(req.RateRPS))
		go drive(ctx, req, appURL, client, lg, state)
		writeJSON(w, state.snapshot())
	})

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("loadgen listening on :%s, target=%s", port, appURL)
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

func applyDefaults(req *startReq, defaultKeyspace int) {
	if req.Mode == "" {
		req.Mode = ModeBaseline
	}
	if req.Dist == "" {
		req.Dist = "zipf"
	}
	if req.Label == "" {
		req.Label = req.Mode
	}
	if req.RateRPS <= 0 {
		req.RateRPS = 2000
	}
	if req.DurationS <= 0 {
		req.DurationS = 60
	}
	if req.KeyspaceSize <= 0 {
		req.KeyspaceSize = defaultKeyspace
	}
	if req.HotKey == "" {
		req.HotKey = "celebrity-1"
	}
	if req.Writers <= 0 {
		req.Writers = 4
	}
	if req.Strategy == "" {
		req.Strategy = "ttl-only"
	}
}

// drive dispatches to the per-mode workload.
func drive(ctx context.Context, req startReq, appURL string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	defer markStopped(state)
	if req.Mode == ModeStaleness {
		driveStaleness(ctx, req, appURL, client, lg, state)
		return
	}
	driveReads(ctx, req, appURL, client, lg, state)
}

func markStopped(state *runState) {
	state.mu.Lock()
	state.running = false
	state.mu.Unlock()
}

// driveReads is the read workload shared by baseline / stampede /
// hotkey. Key selection is done in the single ticker goroutine (Zipf
// generators are not safe for concurrent use), then handed to a worker.
func driveReads(ctx context.Context, req startReq, appURL string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	// s>1 required; 1.07 approximates the alpha~1.0 skew the guide wants.
	zipf := rand.NewZipf(rng, 1.07, 1.0, uint64(req.KeyspaceSize-1))

	tick := time.Duration(float64(time.Second) / float64(req.RateRPS))
	if tick <= 0 {
		tick = time.Microsecond
	}
	deadline := time.Now().Add(time.Duration(req.DurationS) * time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()

	var wg sync.WaitGroup
	limit := make(chan struct{}, 4096)

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case now := <-t.C:
			if now.After(deadline) {
				wg.Wait()
				return
			}
			key := pickKey(req, rng, zipf)
			limit <- struct{}{}
			wg.Add(1)
			go func(k string) {
				defer wg.Done()
				defer func() { <-limit }()
				doRead(ctx, k, req, appURL, client, lg, state)
			}(key)
		}
	}
}

func pickKey(req startReq, rng *rand.Rand, zipf *rand.Zipf) string {
	switch req.Mode {
	case ModeStampede:
		return req.HotKey
	case ModeHotKey:
		if req.HotWeight > 0 && rng.Float64() < req.HotWeight {
			return req.HotKey
		}
	}
	if req.Dist == "uniform" {
		return fmt.Sprintf("item-%d", rng.Intn(req.KeyspaceSize))
	}
	return fmt.Sprintf("item-%d", int(zipf.Uint64()))
}

func doRead(ctx context.Context, key string, req startReq, appURL string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	atomic.AddInt64(&state.offered, 1)
	lg.OfferedTotal.WithLabelValues(req.Label, req.Mode).Inc()

	start := time.Now()
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, appURL+"/read?key="+key, nil)
	resp, err := client.Do(hreq)
	latency := time.Since(start).Seconds()

	code := "ERR"
	if resp != nil {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		code = strconv.Itoa(resp.StatusCode)
	}
	lg.OutcomeTotal.WithLabelValues(req.Label, req.Mode, code).Inc()
	lg.RequestSeconds.WithLabelValues(req.Label, req.Mode, code).Observe(latency)

	if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		atomic.AddInt64(&state.served, 1)
		lg.ServedTotal.WithLabelValues(req.Label, req.Mode).Inc()
		return
	}
	atomic.AddInt64(&state.failed, 1)
}

// driveStaleness runs writers (updating the SoR) and readers (sampling
// through the cache, comparing each read to /source) concurrently for
// the run duration.
func driveStaleness(ctx context.Context, req startReq, appURL string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	const staleKeys = 50
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(req.DurationS)*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Writers: steadily bump versions on the small hot keyset.
	for i := 0; i < req.Writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))
			t := time.NewTicker(200 * time.Millisecond)
			defer t.Stop()
			counter := 0
			for {
				select {
				case <-runCtx.Done():
					return
				case <-t.C:
					key := fmt.Sprintf("item-%d", rng.Intn(staleKeys))
					counter++
					writeKey(runCtx, appURL, client, key, fmt.Sprintf("w%d-%d", id, counter))
				}
			}
		}(i)
	}

	// Readers: drive at the requested rate, compare cache vs source.
	tick := time.Duration(float64(time.Second) / float64(req.RateRPS))
	if tick <= 0 {
		tick = time.Microsecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	limit := make(chan struct{}, 2048)

	for {
		select {
		case <-runCtx.Done():
			wg.Wait()
			return
		case <-t.C:
			key := fmt.Sprintf("item-%d", rng.Intn(staleKeys))
			limit <- struct{}{}
			wg.Add(1)
			go func(k string) {
				defer wg.Done()
				defer func() { <-limit }()
				sampleStaleness(runCtx, k, req, appURL, client, lg, state)
			}(key)
		}
	}
}

func writeKey(ctx context.Context, appURL string, client *http.Client, key, value string) {
	body, _ := json.Marshal(map[string]string{"key": key, "value": value})
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, appURL+"/write", bytes.NewReader(body))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(hreq)
	if err == nil && resp != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

type readResp struct {
	Version int64 `json:"version"`
}
type sourceResp struct {
	Version   int64   `json:"version"`
	UpdatedAt float64 `json:"updated_at"`
}

func sampleStaleness(ctx context.Context, key string, req startReq, appURL string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	atomic.AddInt64(&state.offered, 1)
	lg.OfferedTotal.WithLabelValues(req.Label, req.Mode).Inc()

	cached, ok1 := getJSON[readResp](ctx, client, appURL+"/read?key="+key)
	source, ok2 := getJSON[sourceResp](ctx, client, appURL+"/source?key="+key)
	if !ok1 || !ok2 {
		atomic.AddInt64(&state.failed, 1)
		return
	}
	atomic.AddInt64(&state.served, 1)
	lg.ServedTotal.WithLabelValues(req.Label, req.Mode).Inc()

	if cached.Version == source.Version {
		atomic.AddInt64(&state.fresh, 1)
		lg.StalenessSamplesTotal.WithLabelValues(req.Label, req.Strategy, "fresh").Inc()
		return
	}
	// Stale: the cache is serving an older version than the SoR.
	atomic.AddInt64(&state.stale, 1)
	lg.StalenessSamplesTotal.WithLabelValues(req.Label, req.Strategy, "stale").Inc()

	if source.UpdatedAt > 0 {
		staleSec := time.Since(time.Unix(0, int64(source.UpdatedAt*1e9))).Seconds()
		staleN := int64(staleSec * 1e9)
		for {
			cur := atomic.LoadInt64(&state.maxStaleN)
			if staleN <= cur || atomic.CompareAndSwapInt64(&state.maxStaleN, cur, staleN) {
				break
			}
		}
		lg.StalenessMaxSeconds.WithLabelValues(req.Label, req.Strategy).Set(float64(atomic.LoadInt64(&state.maxStaleN)) / 1e9)
	}
}

func getJSON[T any](ctx context.Context, client *http.Client, url string) (T, bool) {
	var out T
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(hreq)
	if err != nil || resp == nil {
		return out, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return out, false
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, false
	}
	return out, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
