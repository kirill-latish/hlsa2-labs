// loadgen is the in-cluster Go load driver for the edge. It does two
// jobs:
//
//  1. /start drives a representative request mix (static / semi-dynamic
//     / dynamic) at a constant arrival rate, round-robining across the
//     PoPs and injecting tracking params (utm_source/fbclid/gclid) onto
//     a slice of "shared-link" traffic. This is what bench-baseline and
//     bench-cachekey use.
//
//  2. /probe runs the cross-user leak probe: it sends many requests to
//     the personalized route as DIFFERENT users (each its own uid
//     cookie) against a single PoP, and counts how many responses come
//     back personalized for the WRONG user. A nonzero count is a real
//     cache-driven data leak.
//
// HTTP surface:
//
//	POST /start  {"rate_rps":200,"duration_s":120,"label":"baseline"}
//	POST /stop
//	GET  /state
//	GET  /summary
//	POST /probe  {"users":20,"requests":200,"label":"leak-before"}
//	GET  /healthz, /metrics
package main

import (
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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hlsa2-labs/lab6-2/internal/metrics"
)

// catalog defines the object IDs the workload draws from. Popular small
// objects dominate the request count; the rare "big*" objects dominate
// the byte count when they miss.
var (
	popularStatic = []string{"s0", "s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9"}
	bigStatic     = []string{"big0", "big1", "big2", "big3", "big4"}
	pages         = []string{"p0", "p1", "p2", "p3", "p4"}
	apis          = []string{"a0", "a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9"}
	trackingKeys  = []string{"utm_source", "fbclid", "gclid"}
	trackingVals  = []string{"newsletter", "twitter", "facebook", "google", "reddit", "promo"}
)

// PopularObject is the hot object the thundering-herd step expires. The
// expire-popular-object script purges /obj/<PopularObject> on all PoPs.
const PopularObject = "s0"

type startReq struct {
	RateRPS   int    `json:"rate_rps"`
	DurationS int    `json:"duration_s"`
	Label     string `json:"label"`
}

type probeReq struct {
	Users    int    `json:"users"`
	Requests int    `json:"requests"`
	Label    string `json:"label"`
}

type runState struct {
	mu        sync.Mutex
	running   bool
	cancel    context.CancelFunc
	label     string
	startedAt time.Time
	endsAt    time.Time
	rateRPS   int
	durationS int

	offered int64
	served  int64
	failed  int64
}

func (s *runState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"running":    s.running,
		"label":      s.label,
		"started_at": s.startedAt.UTC().Format(time.RFC3339Nano),
		"ends_at":    s.endsAt.UTC().Format(time.RFC3339Nano),
		"rate_rps":   s.rateRPS,
		"duration_s": s.durationS,
		"offered":    atomic.LoadInt64(&s.offered),
		"served":     atomic.LoadInt64(&s.served),
		"failed":     atomic.LoadInt64(&s.failed),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func popURLs() []string {
	raw := envOrDefault("POP_URLS", "http://pop-1:8080,http://pop-2:8080,http://pop-3:8080")
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	port := envOrDefault("PORT", "8090")
	pops := popURLs()
	httpMetrics := metrics.NewHTTPMetrics("loadgen")
	lg := metrics.NewLoadgen()
	state := &runState{}

	client := &http.Client{
		Timeout: 10 * time.Second,
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

	r.Get("/state", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, state.snapshot()) })
	r.Get("/summary", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, state.snapshot()) })

	r.Post("/stop", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		if state.cancel != nil {
			state.cancel()
		}
		state.running = false
		state.mu.Unlock()
		_, _ = w.Write([]byte("stopped"))
	})

	r.Post("/start", func(w http.ResponseWriter, req *http.Request) {
		var body startReq
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.RateRPS <= 0 {
			body.RateRPS = 200
		}
		if body.DurationS <= 0 {
			body.DurationS = 120
		}
		if body.Label == "" {
			body.Label = "baseline"
		}

		state.mu.Lock()
		if state.running && state.cancel != nil {
			state.cancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		state.running = true
		state.cancel = cancel
		state.label = body.Label
		state.startedAt = time.Now()
		state.endsAt = time.Now().Add(time.Duration(body.DurationS) * time.Second)
		state.rateRPS = body.RateRPS
		state.durationS = body.DurationS
		atomic.StoreInt64(&state.offered, 0)
		atomic.StoreInt64(&state.served, 0)
		atomic.StoreInt64(&state.failed, 0)
		state.mu.Unlock()

		lg.RateRPS.WithLabelValues(body.Label).Set(float64(body.RateRPS))
		go drive(ctx, body, pops, client, lg, state)
		writeJSON(w, http.StatusOK, state.snapshot())
	})

	r.Post("/probe", func(w http.ResponseWriter, req *http.Request) {
		var body probeReq
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.Users <= 0 {
			body.Users = 20
		}
		if body.Requests <= 0 {
			body.Requests = 200
		}
		if body.Label == "" {
			body.Label = "leak"
		}
		result := probe(req.Context(), body, pops, client, lg)
		writeJSON(w, http.StatusOK, result)
	})

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("loadgen listening on :%s pops=%v", port, pops)
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

func drive(ctx context.Context, req startReq, pops []string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	tick := time.Duration(float64(time.Second) / float64(req.RateRPS))
	if tick <= 0 {
		tick = time.Millisecond
	}
	deadline := time.Now().Add(time.Duration(req.DurationS) * time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()

	var wg sync.WaitGroup
	limit := make(chan struct{}, 4096)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var counter uint64

	finish := func() {
		wg.Wait()
		state.mu.Lock()
		state.running = false
		state.mu.Unlock()
	}

	for {
		select {
		case <-ctx.Done():
			finish()
			return
		case now := <-t.C:
			if now.After(deadline) {
				finish()
				return
			}
			pop := pops[atomic.AddUint64(&counter, 1)%uint64(len(pops))]
			path, class := pickRequest(rng)
			limit <- struct{}{}
			wg.Add(1)
			go func(pop, path, class string) {
				defer wg.Done()
				defer func() { <-limit }()
				doOne(ctx, req.Label, pop, path, class, client, lg, state)
			}(pop, path, class)
		}
	}
}

// pickRequest returns a URL path (with any tracking params) and its
// class label. Roughly 60% static, 25% page, 15% api; within static,
// ~85% popular-small and ~15% big-rare; ~30% of static+page traffic is
// "shared-link" and carries tracking params.
func pickRequest(rng *rand.Rand) (string, string) {
	roll := rng.Float64()
	switch {
	case roll < 0.60:
		var id string
		if rng.Float64() < 0.85 {
			id = popularStatic[rng.Intn(len(popularStatic))]
		} else {
			id = bigStatic[rng.Intn(len(bigStatic))]
		}
		return withTracking(rng, "/obj/"+id, 0.30), "static"
	case roll < 0.85:
		id := pages[rng.Intn(len(pages))]
		path := "/page/" + id
		// pages sometimes carry a real content-affecting version param.
		if rng.Float64() < 0.5 {
			path += "?v=" + strconv.Itoa(rng.Intn(3))
		}
		return withTracking(rng, path, 0.30), "page"
	default:
		id := apis[rng.Intn(len(apis))]
		return "/api/" + id, "api"
	}
}

func withTracking(rng *rand.Rand, path string, prob float64) string {
	if rng.Float64() >= prob {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	k := trackingKeys[rng.Intn(len(trackingKeys))]
	v := trackingVals[rng.Intn(len(trackingVals))]
	return path + sep + k + "=" + v
}

func doOne(ctx context.Context, label, pop, path, class string, client *http.Client, lg *metrics.Loadgen, state *runState) {
	atomic.AddInt64(&state.offered, 1)
	lg.OfferedTotal.WithLabelValues(label, class).Inc()

	start := time.Now()
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, pop+path, nil)
	resp, err := client.Do(hreq)
	latency := time.Since(start).Seconds()

	code := "ERR"
	if resp != nil {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		code = strconv.Itoa(resp.StatusCode)
	}
	lg.RequestSeconds.WithLabelValues(label, code).Observe(latency)
	if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		atomic.AddInt64(&state.served, 1)
		lg.ServedTotal.WithLabelValues(label, class).Inc()
		return
	}
	atomic.AddInt64(&state.failed, 1)
}

// probe sends `requests` requests to the personalized route as a rotating
// set of `users`, against a single PoP (so the broad-key cache is shared
// among them). It parses the personalized_for field and counts responses
// served for the WRONG user.
func probe(ctx context.Context, req probeReq, pops []string, client *http.Client, lg *metrics.Loadgen) map[string]any {
	target := pops[0]
	leaked, clean, errced := 0, 0, 0
	for i := 0; i < req.Requests; i++ {
		uid := fmt.Sprintf("user-%d", i%req.Users)
		hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, target+"/account", nil)
		hreq.AddCookie(&http.Cookie{Name: "uid", Value: uid})
		resp, err := client.Do(hreq)
		if err != nil || resp == nil {
			errced++
			continue
		}
		var body struct {
			PersonalizedFor string `json:"personalized_for"`
		}
		dec := json.NewDecoder(resp.Body)
		_ = dec.Decode(&body)
		resp.Body.Close()

		if body.PersonalizedFor != "" && body.PersonalizedFor != uid {
			leaked++
			lg.CrossUserLeak.Inc()
			lg.ProbeTotal.WithLabelValues(req.Label, "leak").Inc()
		} else {
			clean++
			lg.ProbeTotal.WithLabelValues(req.Label, "clean").Inc()
		}
	}
	log.Printf("probe[%s]: users=%d requests=%d leaked=%d clean=%d err=%d", req.Label, req.Users, req.Requests, leaked, clean, errced)
	return map[string]any{
		"label":    req.Label,
		"users":    req.Users,
		"requests": req.Requests,
		"leaked":   leaked,
		"clean":    clean,
		"errors":   errced,
		"target":   target,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
