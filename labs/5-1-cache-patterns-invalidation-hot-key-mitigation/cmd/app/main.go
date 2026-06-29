// app is the cache-aside / write-through application under test. It
// sits between a sharded Redis cache (3 standalone nodes, client-side
// sharded - see internal/shard) and a Postgres system of record with a
// small simulated query latency.
//
// Every cache pattern the topic guide teaches is a runtime-flippable
// knob set via POST /admin/config (no restart between bench runs):
//
//	cache_ttl_seconds       base TTL written to Redis on a miss
//	ttl_jitter_pct          spread expiry so keys don't expire together
//	coalescing              none|singleflight|xfetch|swr
//	local_lru / size / ttl  in-process LRU that short-circuits hot keys
//	invalidation            ttl-only|explicit-invalidate
//
// Read endpoints the loadgen drives:
//
//	GET  /read?key=K     cache-aside read (the hot path)
//	GET  /source?key=K   bypass the cache, read the SoR (staleness probe)
//	POST /write          update the SoR + invalidate per strategy
//
// Admin endpoints:
//
//	GET/POST /admin/config   read / patch the runtime knobs
//	POST     /admin/warm     warm an initial key set into Redis
//	POST     /admin/flush    FLUSHDB all shards + purge the local LRU
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"github.com/hlsa2-labs/lab5-1/internal/lru"
	"github.com/hlsa2-labs/lab5-1/internal/metrics"
	"github.com/hlsa2-labs/lab5-1/internal/shard"
)

// Coalescing modes.
const (
	CoalesceNone        = "none"
	CoalesceSingleflght = "singleflight"
	CoalesceXFetch      = "xfetch"
	CoalesceSWR         = "swr"
)

// Invalidation strategies.
const (
	InvalTTLOnly  = "ttl-only"
	InvalExplicit = "explicit-invalidate"
)

// xfetchBeta tunes the probabilistic early-refresh aggressiveness.
const xfetchBeta = 1.0

type config struct {
	TTLSeconds      float64 `json:"cache_ttl_seconds"`
	JitterPct       float64 `json:"ttl_jitter_pct"`
	Coalescing      string  `json:"coalescing"`
	LocalLRU        bool    `json:"local_lru"`
	LocalSize       int     `json:"local_lru_size"`
	LocalTTLSeconds float64 `json:"local_lru_ttl_seconds"`
	Invalidation    string  `json:"invalidation"`
}

// envelope is the JSON value stored in Redis. It carries enough
// metadata (stored-at, soft TTL, recompute delta) for xfetch and SWR
// to reason about age without a second round-trip.
type envelope struct {
	Value    string  `json:"v"`
	Version  int64   `json:"ver"`
	StoredAt int64   `json:"sa"`
	SoftTTL  float64 `json:"soft"`
	Delta    float64 `json:"delta"`
}

type app struct {
	cfgMu sync.RWMutex
	cfg   config

	ring  *shard.Ring
	pg    *pgxpool.Pool
	local *lru.Cache
	sf    singleflight.Group
	cm    *metrics.Cache

	sourceLatency time.Duration
	sourceJitter  time.Duration
}

func (a *app) snapshot() config {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.cfg
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
	pgDSN := envOrDefault("POSTGRES_DSN", "postgres://hlsa:hlsa@postgres:5432/hlsa?sslmode=disable")

	rand.New(rand.NewSource(time.Now().UnixNano()))

	// Build the client-side shard ring from REDIS_NODES (comma list of
	// name=addr pairs), defaulting to the three compose nodes.
	names, addrs := parseRedisNodes(envOrDefault("REDIS_NODES",
		"redis-1=redis-1:6379,redis-2=redis-2:6379,redis-3=redis-3:6379"))
	clients := make([]*redis.Client, len(addrs))
	for i, addr := range addrs {
		clients[i] = redis.NewClient(&redis.Options{Addr: addr})
	}
	ring := shard.New(names, clients)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("app: postgres connect: %v", err)
	}

	cm := metrics.NewCache()
	a := &app{
		cfg: config{
			TTLSeconds:      float64(envInt("CACHE_TTL_SECONDS", 60)),
			JitterPct:       0,
			Coalescing:      CoalesceNone,
			LocalLRU:        false,
			LocalSize:       envInt("LOCAL_LRU_SIZE", 1000),
			LocalTTLSeconds: 5,
			Invalidation:    InvalTTLOnly,
		},
		ring:          ring,
		pg:            pg,
		cm:            cm,
		sourceLatency: time.Duration(envInt("SOURCE_LATENCY_MS", 30)) * time.Millisecond,
		sourceJitter:  time.Duration(envInt("SOURCE_JITTER_MS", 20)) * time.Millisecond,
	}
	a.local = lru.New(a.cfg.LocalSize, time.Duration(a.cfg.LocalTTLSeconds*float64(time.Second)))
	a.publishConfig(a.snapshot())

	httpMetrics := metrics.NewHTTPMetrics("app")

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", metrics.Handler())

	r.Get("/read", a.handleRead)
	r.Get("/source", a.handleSource)
	r.Post("/write", a.handleWrite)

	r.Get("/admin/config", a.handleGetConfig)
	r.Post("/admin/config", a.handlePostConfig)
	r.Post("/admin/warm", a.handleWarm)
	r.Post("/admin/flush", a.handleFlush)

	// Periodically publish local-LRU occupancy + cumulative evictions.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		var lastEvict int64
		for range t.C {
			a.cm.LocalSizeGauge.Set(float64(a.local.Len()))
			ev := a.local.Evictions()
			if d := ev - lastEvict; d > 0 {
				a.cm.EvictionsTotal.WithLabelValues("local").Add(float64(d))
				lastEvict = ev
			}
		}
	}()

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("app listening on :%s shards=%v source_latency=%s", port, names, a.sourceLatency)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	_ = srv.Shutdown(shutdownCtx)
}

func parseRedisNodes(spec string) (names, addrs []string) {
	for _, pair := range splitComma(spec) {
		name, addr := splitEq(pair)
		if name == "" || addr == "" {
			continue
		}
		names = append(names, name)
		addrs = append(addrs, addr)
	}
	return names, addrs
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func splitEq(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:]
		}
	}
	return "", ""
}

// --- read path -------------------------------------------------------

func (a *app) handleRead(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}
	cfg := a.snapshot()
	start := time.Now()

	// 1) Local LRU short-circuit (the hot-key mitigation).
	if cfg.LocalLRU {
		if v, ver, ok := a.local.Get(key); ok {
			a.observeRead("local_hit", start)
			writeJSON(w, map[string]any{"key": key, "value": v, "version": ver, "outcome": "local_hit", "from_local": true})
			return
		}
	}

	rc, node := a.ring.For(key)
	a.cm.RedisOpsTotal.WithLabelValues(node, "get").Inc()
	raw, err := rc.Get(r.Context(), key).Result()

	if err == nil {
		// Cache hit. Decode envelope to apply xfetch / swr logic.
		var env envelope
		if jsonErr := json.Unmarshal([]byte(raw), &env); jsonErr == nil {
			outcome := a.onHit(r.Context(), key, env, cfg)
			if cfg.LocalLRU {
				a.local.Add(key, env.Value, env.Version)
			}
			a.observeRead(outcome, start)
			writeJSON(w, map[string]any{"key": key, "value": env.Value, "version": env.Version, "outcome": outcome, "node": node})
			return
		}
	} else if !errors.Is(err, redis.Nil) {
		// Treat a transport error as a miss but don't crash the run.
		log.Printf("app: redis get %s on %s: %v", key, node, err)
	}

	// 2) Miss: fetch from the SoR, with coalescing per the active mode.
	a.cm.MissesTotal.Inc()
	value, version := a.fetchOnMiss(r.Context(), key, cfg)
	a.observeRead("miss", start)
	writeJSON(w, map[string]any{"key": key, "value": value, "version": version, "outcome": "miss", "node": node})
}

// onHit applies xfetch/swr freshness logic to a hit and returns the
// outcome label. It may trigger an async background refresh.
func (a *app) onHit(ctx context.Context, key string, env envelope, cfg config) string {
	age := time.Since(time.Unix(0, env.StoredAt)).Seconds()
	switch cfg.Coalescing {
	case CoalesceSWR:
		if age >= env.SoftTTL {
			a.refreshAsync(key, cfg)
			return "stale_hit"
		}
	case CoalesceXFetch:
		// XFetch: refresh early with rising probability as the value
		// ages toward its soft TTL. Keeps the hot key from ever hard-
		// expiring under load, which is what causes the stampede.
		delta := env.Delta
		if delta <= 0 {
			delta = 0.05
		}
		gate := age + delta*xfetchBeta*(-math.Log(rand.Float64()))
		if gate >= env.SoftTTL {
			a.refreshAsync(key, cfg)
		}
	}
	return "hit"
}

// fetchOnMiss resolves a miss, coalescing concurrent misses for the
// same key when the active mode asks for it. With CoalesceNone every
// concurrent miss does its own SoR fetch - that is the stampede.
func (a *app) fetchOnMiss(ctx context.Context, key string, cfg config) (string, int64) {
	if cfg.Coalescing == CoalesceNone {
		env := a.fetchAndStore(ctx, key, cfg, true)
		return env.Value, env.Version
	}
	v, _, _ := a.sf.Do(key, func() (any, error) {
		return a.fetchAndStore(ctx, key, cfg, true), nil
	})
	env := v.(envelope)
	return env.Value, env.Version
}

// refreshAsync recomputes a key in the background, coalesced through
// singleflight so a thundering herd of refresh triggers collapses to a
// single SoR fetch.
func (a *app) refreshAsync(key string, cfg config) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = a.sf.Do("refresh:"+key, func() (any, error) {
			return a.fetchAndStore(ctx, key, cfg, true), nil
		})
	}()
}

// fetchAndStore reads the SoR and writes the result back into the
// key's shard with the (jittered) TTL. simulate controls whether the
// artificial query latency is applied (off for warm-up).
func (a *app) fetchAndStore(ctx context.Context, key string, cfg config, simulate bool) envelope {
	fstart := time.Now()
	value, version := a.fetchFromSource(ctx, key, simulate)
	a.cm.SourceFetchesTotal.Inc()
	delta := time.Since(fstart).Seconds()
	a.cm.SourceFetchSeconds.Observe(delta)

	soft := a.ttlWithJitter(cfg)
	env := envelope{Value: value, Version: version, StoredAt: time.Now().UnixNano(), SoftTTL: soft, Delta: delta}
	blob, _ := json.Marshal(env)

	// xfetch/swr need the value to outlive its soft TTL so the app can
	// govern refresh; everyone else lets Redis expire it at soft TTL.
	redisTTL := time.Duration(soft * float64(time.Second))
	if cfg.Coalescing == CoalesceXFetch || cfg.Coalescing == CoalesceSWR {
		redisTTL = time.Duration(soft * 2 * float64(time.Second))
	}

	rc, node := a.ring.For(key)
	a.cm.RedisOpsTotal.WithLabelValues(node, "set").Inc()
	if err := rc.Set(ctx, key, blob, redisTTL).Err(); err != nil {
		log.Printf("app: redis set %s on %s: %v", key, node, err)
	}
	if cfg.LocalLRU {
		a.local.Add(key, value, version)
	}
	return env
}

// fetchFromSource reads the system of record. The simulated latency is
// what makes a stampede observable: during the fetch window many
// concurrent misses pile up against the database.
func (a *app) fetchFromSource(ctx context.Context, key string, simulate bool) (string, int64) {
	if simulate {
		sleep := a.sourceLatency
		if a.sourceJitter > 0 {
			sleep += time.Duration(rand.Int63n(int64(a.sourceJitter)))
		}
		time.Sleep(sleep)
	}
	var value string
	var version int64
	err := a.pg.QueryRow(ctx,
		`SELECT value, version FROM cache_items WHERE key=$1`, key).
		Scan(&value, &version)
	if err != nil {
		// Unseeded key: synthesize a deterministic value so reads still
		// work. Version 0 means "never written".
		return "seed:" + key, 0
	}
	return value, version
}

func (a *app) ttlWithJitter(cfg config) float64 {
	base := cfg.TTLSeconds
	if base <= 0 {
		base = 1
	}
	if cfg.JitterPct <= 0 {
		return base
	}
	// Positive spread: TTL in [base, base*(1+jitter)] so no key expires
	// earlier than the configured TTL but the herd is desynchronised.
	return base * (1 + rand.Float64()*(cfg.JitterPct/100.0))
}

func (a *app) observeRead(outcome string, start time.Time) {
	a.cm.RequestsTotal.WithLabelValues(outcome).Inc()
	a.cm.ReadSeconds.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
}

// --- source probe ----------------------------------------------------

func (a *app) handleSource(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}
	var value string
	var version int64
	var updatedAt time.Time
	err := a.pg.QueryRow(r.Context(),
		`SELECT value, version, updated_at FROM cache_items WHERE key=$1`, key).
		Scan(&value, &version, &updatedAt)
	if err != nil {
		writeJSON(w, map[string]any{"key": key, "value": "seed:" + key, "version": int64(0), "updated_at": float64(0)})
		return
	}
	writeJSON(w, map[string]any{
		"key": key, "value": value, "version": version,
		"updated_at": float64(updatedAt.UnixNano()) / 1e9,
	})
}

// --- write path ------------------------------------------------------

func (a *app) handleWrite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
		http.Error(w, "key and value are required", http.StatusBadRequest)
		return
	}
	cfg := a.snapshot()

	var version int64
	err := a.pg.QueryRow(r.Context(), `
		INSERT INTO cache_items (key, value, version, updated_at)
		VALUES ($1, $2, 1, now())
		ON CONFLICT (key) DO UPDATE
		  SET value = EXCLUDED.value,
		      version = cache_items.version + 1,
		      updated_at = now()
		RETURNING version`, body.Key, body.Value).Scan(&version)
	if err != nil {
		http.Error(w, "write failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Invalidation strategy decides whether the cache is purged now.
	if cfg.Invalidation == InvalExplicit {
		rc, node := a.ring.For(body.Key)
		a.cm.RedisOpsTotal.WithLabelValues(node, "del").Inc()
		_ = rc.Del(r.Context(), body.Key).Err()
		a.local.Delete(body.Key)
	}

	writeJSON(w, map[string]any{"key": body.Key, "version": version, "invalidation": cfg.Invalidation})
}

// --- admin endpoints -------------------------------------------------

func (a *app) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, a.snapshot())
}

func (a *app) handlePostConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TTLSeconds      *float64 `json:"cache_ttl_seconds"`
		JitterPct       *float64 `json:"ttl_jitter_pct"`
		Coalescing      *string  `json:"coalescing"`
		LocalLRU        *bool    `json:"local_lru"`
		LocalSize       *int     `json:"local_lru_size"`
		LocalTTLSeconds *float64 `json:"local_lru_ttl_seconds"`
		Invalidation    *string  `json:"invalidation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.cfgMu.Lock()
	next := a.cfg
	if body.TTLSeconds != nil {
		next.TTLSeconds = *body.TTLSeconds
	}
	if body.JitterPct != nil {
		next.JitterPct = *body.JitterPct
	}
	if body.Coalescing != nil {
		next.Coalescing = *body.Coalescing
	}
	if body.LocalLRU != nil {
		next.LocalLRU = *body.LocalLRU
	}
	if body.LocalSize != nil {
		next.LocalSize = *body.LocalSize
	}
	if body.LocalTTLSeconds != nil {
		next.LocalTTLSeconds = *body.LocalTTLSeconds
	}
	if body.Invalidation != nil {
		next.Invalidation = *body.Invalidation
	}
	a.cfg = next
	a.cfgMu.Unlock()

	a.local.Reconfigure(next.LocalSize, time.Duration(next.LocalTTLSeconds*float64(time.Second)))
	a.publishConfig(next)
	log.Printf("admin/config: %+v", next)
	writeJSON(w, next)
}

func (a *app) handleWarm(w http.ResponseWriter, r *http.Request) {
	count := envInt("WARM_COUNT", 2000)
	if q := r.URL.Query().Get("count"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			count = n
		}
	}
	cfg := a.snapshot()
	const workers = 32
	jobs := make(chan int, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for n := range jobs {
				key := fmt.Sprintf("item-%d", n)
				a.fetchAndStore(ctx, key, cfg, false)
			}
		}()
	}
	for n := 0; n < count; n++ {
		jobs <- n
	}
	close(jobs)
	wg.Wait()
	writeJSON(w, map[string]any{"warmed": count})
}

func (a *app) handleFlush(w http.ResponseWriter, r *http.Request) {
	for i, rc := range a.ring.Clients() {
		if err := rc.FlushDB(r.Context()).Err(); err != nil {
			log.Printf("app: flush %s: %v", a.ring.Names()[i], err)
		}
	}
	a.local.Purge()
	writeJSON(w, map[string]any{"flushed": a.ring.Names()})
}

func (a *app) publishConfig(cfg config) {
	a.cm.ConfigTTLSeconds.Set(cfg.TTLSeconds)
	a.cm.ConfigJitterPct.Set(cfg.JitterPct)
	localStr := "off"
	if cfg.LocalLRU {
		a.cm.ConfigLocalLRU.Set(1)
		localStr = "on"
	} else {
		a.cm.ConfigLocalLRU.Set(0)
	}
	a.cm.ConfigInfo.Reset()
	a.cm.ConfigInfo.WithLabelValues(cfg.Coalescing, cfg.Invalidation, localStr).Set(1)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
