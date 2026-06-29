// origin is the single backend the whole edge sits in front of. It
// serves a small catalog of objects with configurable size and latency,
// personalized account responses, and two failure injections the lab
// drives from the make targets:
//
//	outage              every content route returns 503 (for the
//	                    stale-if-error graceful-degradation demo)
//	setcookie_on_static a Set-Cookie header is glued onto static
//	                    responses (the "caching nothing" silent failure -
//	                    the edge is forced to BYPASS what should be cached)
//
// Object sizing is deterministic by ID so hit-ratio-by-request and
// hit-ratio-by-bytes diverge: popular objects are small (lots of cheap
// hits) and the rare long-tail objects are large (every miss is
// expensive), exactly the asymmetry the baseline step asks you to
// separate.
//
// HTTP surface:
//
//	GET  /obj/{id}     static asset       (Cache-Control: public)
//	GET  /page/{id}    semi-dynamic page  (Cache-Control: public, shorter)
//	GET  /api/{id}     dynamic JSON       (Cache-Control: public, short)
//	GET  /account      personalized       (Cache-Control: private, no-store)
//	GET  /healthz, /metrics
//	POST /admin/config {"outage":bool,"setcookie_on_static":bool,"base_latency_ms":int}
//	GET  /admin/config
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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hlsa2-labs/lab6-2/internal/metrics"
)

type config struct {
	Outage            bool `json:"outage"`
	SetCookieOnStatic bool `json:"setcookie_on_static"`
	BaseLatencyMS     int  `json:"base_latency_ms"`
	JitterMS          int  `json:"jitter_ms"`
}

type server struct {
	mu  sync.RWMutex
	cfg config
	om  *metrics.Origin
}

func (s *server) snapshot() config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
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
	port := envOrDefault("PORT", "8088")
	s := &server{
		cfg: config{
			Outage:            false,
			SetCookieOnStatic: false,
			BaseLatencyMS:     envInt("BASE_LATENCY_MS", 40),
			JitterMS:          envInt("JITTER_MS", 20),
		},
		om: metrics.NewOrigin(),
	}
	s.om.OutageActive.Set(0)
	s.om.SetCookieStatic.Set(0)

	httpMetrics := metrics.NewHTTPMetrics("origin")

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", metrics.Handler())

	r.Get("/admin/config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.snapshot())
	})
	r.Post("/admin/config", s.handleConfig)

	r.Get("/obj/{id}", s.handleObject("static"))
	r.Get("/page/{id}", s.handleObject("page"))
	r.Get("/api/{id}", s.handleObject("api"))
	r.Get("/account", s.handleAccount)

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("origin listening on :%s base=%dms jitter=%dms", port, s.cfg.BaseLatencyMS, s.cfg.JitterMS)
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

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Outage            *bool `json:"outage"`
		SetCookieOnStatic *bool `json:"setcookie_on_static"`
		BaseLatencyMS     *int  `json:"base_latency_ms"`
		JitterMS          *int  `json:"jitter_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if body.Outage != nil {
		s.cfg.Outage = *body.Outage
	}
	if body.SetCookieOnStatic != nil {
		s.cfg.SetCookieOnStatic = *body.SetCookieOnStatic
	}
	if body.BaseLatencyMS != nil {
		s.cfg.BaseLatencyMS = *body.BaseLatencyMS
	}
	if body.JitterMS != nil {
		s.cfg.JitterMS = *body.JitterMS
	}
	next := s.cfg
	s.mu.Unlock()

	b2f := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	s.om.OutageActive.Set(b2f(next.Outage))
	s.om.SetCookieStatic.Set(b2f(next.SetCookieOnStatic))
	log.Printf("admin/config: %+v", next)
	writeJSON(w, http.StatusOK, next)
}

// objectSize returns a deterministic byte size for an object ID. IDs
// that start with "big" are the large long-tail; everything else is a
// small popular asset. This is what makes by-request and by-bytes hit
// ratios diverge.
func objectSize(class, id string) int {
	if strings.HasPrefix(id, "big") {
		return 256 * 1024
	}
	switch class {
	case "static":
		return 2 * 1024
	case "page":
		return 16 * 1024
	default: // api
		return 1 * 1024
	}
}

func (s *server) sleep(cfg config) {
	d := time.Duration(cfg.BaseLatencyMS) * time.Millisecond
	if cfg.JitterMS > 0 {
		d += time.Duration(rand.Intn(cfg.JitterMS)) * time.Millisecond
	}
	time.Sleep(d)
}

func (s *server) handleObject(class string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := s.snapshot()
		if cfg.Outage {
			http.Error(w, "origin outage (injected)", http.StatusServiceUnavailable)
			return
		}
		id := chi.URLParam(r, "id")
		s.om.Requests.WithLabelValues(class).Inc()
		s.om.ObjectRequests.WithLabelValues(id).Inc()
		s.sleep(cfg)

		size := objectSize(class, id)
		body := make([]byte, size)
		// Cheap deterministic fill so the body isn't all-zero.
		for i := range body {
			body[i] = byte('a' + (i % 26))
		}

		maxAge := 60
		if class == "page" {
			maxAge = 30
		} else if class == "api" {
			maxAge = 10
		}
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Object-Class", class)
		w.Header().Set("X-Object-Id", id)
		// The silent-failure injection: a Set-Cookie on cacheable
		// static content forces every edge to BYPASS.
		if class == "static" && cfg.SetCookieOnStatic {
			w.Header().Set("Set-Cookie", "sessionid="+strconv.Itoa(rand.Intn(1_000_000))+"; Path=/")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

// handleAccount returns content personalized for the uid cookie. It is
// always marked private/no-store at the origin; whether the edge
// respects that is the personalized_mode policy under test.
func (s *server) handleAccount(w http.ResponseWriter, r *http.Request) {
	cfg := s.snapshot()
	if cfg.Outage {
		http.Error(w, "origin outage (injected)", http.StatusServiceUnavailable)
		return
	}
	uid := "anonymous"
	if c, err := r.Cookie("uid"); err == nil && c.Value != "" {
		uid = c.Value
	}
	s.om.Requests.WithLabelValues("account").Inc()
	s.sleep(cfg)

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Personalized", uid)
	writeJSON(w, http.StatusOK, map[string]any{
		"personalized_for": uid,
		"greeting":         "Hello, " + uid,
		"balance":          rand.Intn(10000),
		"served_at":        time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
