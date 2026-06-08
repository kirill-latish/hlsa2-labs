// loadgen drives writes to:
//
//   - Postgres (system of record): users, products, orders
//   - Mongo (sharded): events_<strategy>
//
// SHARD_KEY decides which collection events land in. A control HTTP API
// exposes /run for the bench drivers to start/stop a fixed-rate workload,
// /stats for an at-a-glance snapshot, and /healthz for compose.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/hlsa2-labs/lab4-3/internal/fault"
	"github.com/hlsa2-labs/lab4-3/internal/metrics"
	"github.com/hlsa2-labs/lab4-3/internal/mongoutil"
	"github.com/hlsa2-labs/lab4-3/internal/payloads"
	"github.com/hlsa2-labs/lab4-3/internal/shardkey"
	"github.com/hlsa2-labs/lab4-3/internal/svchelp"
)

type config struct {
	port              string
	postgresDSN       string
	mongoHosts        string
	mongoDB           string
	faultURL          string
	hotSlot           string
	defaultStrategy   shardkey.Strategy
	tenantCount       int
	zipfS             float64
	hashSuffixBuckets int
}

type runRequest struct {
	WriteRate        int     `json:"write_rate"`
	DurationSeconds  int     `json:"duration_seconds"`
	ShardKey         string  `json:"shard_key,omitempty"`
	WriteFraction    float64 `json:"write_fraction,omitempty"`
	HashSuffixBucket int     `json:"hash_suffix_buckets,omitempty"`
}

type runStats struct {
	StartedAt    time.Time `json:"started_at"`
	StoppedAt    time.Time `json:"stopped_at"`
	TargetRate   int       `json:"target_rate"`
	Duration     string    `json:"duration"`
	ShardKey     string    `json:"shard_key"`
	WritesOK     uint64    `json:"writes_ok"`
	WritesErr    uint64    `json:"writes_err"`
	HotShareSeen float64   `json:"hot_share_seen"`
	HotEntity    string    `json:"hot_entity"`
}

func main() {
	cfg := loadConfig()
	ctx, cancel := svchelp.SignalContext()
	defer cancel()

	pg, err := pgxpool.New(ctx, cfg.postgresDSN)
	if err != nil {
		log.Fatalf("loadgen: postgres connect: %v", err)
	}
	defer pg.Close()

	mg, err := mongoutil.ConnectMongos(ctx, cfg.mongoHosts)
	if err != nil {
		log.Fatalf("loadgen: mongo connect: %v", err)
	}
	defer mg.Disconnect(context.Background())

	fc := fault.New(cfg.faultURL)

	writesOK := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "lab43_loadgen_writes_total",
		Help: "Writes attempted by the loadgen, partitioned by outcome and shard-key strategy.",
	}, []string{"outcome", "shard_key"})
	hotShare := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "lab43_loadgen_hot_share",
		Help: "Fraction of recent writes that targeted the hot entity.",
	}, []string{"entity"})
	writeLatency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "lab43_loadgen_write_latency_ms",
		Help:    "End-to-end write latency in milliseconds (postgres + mongo).",
		Buckets: prometheus.ExponentialBuckets(1, 2, 12),
	}, []string{"shard_key"})
	metrics.MustRegister(writesOK, hotShare, writeLatency)

	gen := &generator{
		cfg:          cfg,
		pg:           pg,
		mg:           mg,
		fault:        fc,
		writesOK:     writesOK,
		hotShare:     hotShare,
		writeLatency: writeLatency,
		strategy:     cfg.defaultStrategy,
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Get("/stats", func(w http.ResponseWriter, _ *http.Request) {
		svchelp.WriteOK(w, gen.snapshot())
	})
	r.Post("/run", gen.handleRun)
	r.Post("/stop", gen.handleStop)
	r.Handle("/metrics", metrics.Handler())

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("loadgen listening on :%s, default strategy=%s, tenants=%d, zipf_s=%.2f, hash_suffix_buckets=%d",
			cfg.port, cfg.defaultStrategy, cfg.tenantCount, cfg.zipfS, cfg.hashSuffixBuckets)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("loadgen listen: %v", err)
		}
	}()

	<-ctx.Done()
	gen.stop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}

type generator struct {
	cfg          config
	pg           *pgxpool.Pool
	mg           *mongo.Client
	fault        *fault.Client
	writesOK     *prometheus.CounterVec
	hotShare     *prometheus.GaugeVec
	writeLatency *prometheus.HistogramVec

	mu        sync.Mutex
	cancelRun context.CancelFunc
	wg        sync.WaitGroup
	strategy  shardkey.Strategy
	last      runStats
	hotSeen   atomic.Uint64
	totalSeen atomic.Uint64
}

func (g *generator) handleRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.WriteRate <= 0 {
		http.Error(w, "write_rate must be > 0", http.StatusBadRequest)
		return
	}
	strat := g.cfg.defaultStrategy
	if req.ShardKey != "" {
		s, err := shardkey.Parse(req.ShardKey, readFixedFallback())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		strat = s
	}
	d := time.Duration(req.DurationSeconds) * time.Second
	if d <= 0 {
		d = 30 * time.Second
	}
	buckets := req.HashSuffixBucket
	if buckets <= 0 {
		buckets = g.cfg.hashSuffixBuckets
	}
	g.mu.Lock()
	if g.cancelRun != nil {
		g.cancelRun()
		g.cancelRun = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), d+5*time.Second)
	g.cancelRun = cancel
	g.strategy = strat
	g.last = runStats{StartedAt: time.Now().UTC(), TargetRate: req.WriteRate, Duration: d.String(), ShardKey: string(strat)}
	g.hotSeen.Store(0)
	g.totalSeen.Store(0)
	g.mu.Unlock()

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.run(ctx, req.WriteRate, d, strat, buckets)
	}()
	svchelp.WriteOK(w, map[string]any{
		"started":  true,
		"shard_key": strat,
		"buckets":  buckets,
		"duration": d.String(),
	})
}

func (g *generator) handleStop(w http.ResponseWriter, _ *http.Request) {
	g.stop()
	svchelp.WriteOK(w, g.snapshot())
}

func (g *generator) stop() {
	g.mu.Lock()
	cancel := g.cancelRun
	g.cancelRun = nil
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	g.wg.Wait()
}

func (g *generator) snapshot() runStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.last
	tot := g.totalSeen.Load()
	if tot > 0 {
		s.HotShareSeen = float64(g.hotSeen.Load()) / float64(tot)
	}
	return s
}

func (g *generator) run(ctx context.Context, rate int, dur time.Duration, strat shardkey.Strategy, buckets int) {
	collName, err := shardkey.CollectionFor(strat)
	if err != nil {
		log.Printf("loadgen: bad strategy: %v", err)
		return
	}
	hot := g.fault.Get(g.cfg.hotSlot)
	builder := shardkey.NewBuilder(strat, buckets, hot.Entity)

	coll := g.mg.Database(g.cfg.mongoDB).Collection(collName)
	tickInterval := time.Second / time.Duration(rate)
	if tickInterval <= 0 {
		tickInterval = time.Millisecond
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	zipf := rand.NewZipf(rng, g.cfg.zipfS, 1, uint64(g.cfg.tenantCount))
	deadline := time.Now().Add(dur)
	var counter int64
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		hot = g.fault.Get(g.cfg.hotSlot)
		builder.HotTenantID = hot.Entity
		ev := g.craftEvent(rng, zipf, hot)
		builder.Apply(&ev, counter)
		counter++
		g.totalSeen.Add(1)
		if hot.Mode == fault.ModeHot && ev.TenantID == hot.Entity {
			g.hotSeen.Add(1)
		}
		start := time.Now()
		err := g.persist(ctx, coll, &ev)
		latencyMS := float64(time.Since(start).Milliseconds())
		labels := prometheus.Labels{"shard_key": string(strat)}
		g.writeLatency.With(labels).Observe(latencyMS)
		if err != nil {
			g.writesOK.With(prometheus.Labels{"outcome": "err", "shard_key": string(strat)}).Inc()
		} else {
			g.writesOK.With(prometheus.Labels{"outcome": "ok", "shard_key": string(strat)}).Inc()
		}
		if hot.Entity != "" {
			g.hotShare.With(prometheus.Labels{"entity": hot.Entity}).Set(g.snapshot().HotShareSeen)
		}
		// Pace.
		if tickInterval > 0 {
			sleep := tickInterval - time.Since(start)
			if sleep > 0 {
				time.Sleep(sleep)
			}
		}
	}
	g.mu.Lock()
	g.last.StoppedAt = time.Now().UTC()
	g.last.HotEntity = hot.Entity
	g.last.WritesOK = g.totalSeen.Load()
	g.mu.Unlock()
}

func (g *generator) craftEvent(rng *rand.Rand, zipf *rand.Zipf, hot fault.Spec) payloads.MongoEvent {
	tenantIdx := zipf.Uint64()
	tenantID := tenantNameFor(int(tenantIdx))
	if hot.Mode == fault.ModeHot && rng.Float64() < hot.Weight {
		tenantID = hot.Entity
	}
	userID := int64(rng.Intn(1_000_000)) + 1
	productID := int64(rng.Intn(10_000)) + 1
	qty := rng.Intn(5) + 1
	price := int64(rng.Intn(10_000)) + 100
	return payloads.MongoEvent{
		EventID:    uuid.NewString(),
		TenantID:   tenantID,
		UserID:     userID,
		ProductID:  productID,
		Quantity:   qty,
		TotalCents: price * int64(qty),
		OccurredAt: time.Now().UTC(),
	}
}

func (g *generator) persist(ctx context.Context, coll *mongo.Collection, ev *payloads.MongoEvent) error {
	wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	tx, err := g.pg.Begin(wctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(wctx) }()
	var orderID int64
	if err := tx.QueryRow(wctx, `
        INSERT INTO orders (tenant_id, user_id, product_id, quantity, total_cents, status, committed_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, 'placed', now(), now())
        RETURNING id
    `, ev.TenantID, ev.UserID, ev.ProductID, ev.Quantity, ev.TotalCents).Scan(&orderID); err != nil {
		return err
	}
	if err := tx.Commit(wctx); err != nil {
		return err
	}

	doc := bson.M{
		"event_id":   ev.EventID,
		"tenant_id":  ev.TenantID,
		"user_id":    ev.UserID,
		"product_id": ev.ProductID,
		"quantity":   ev.Quantity,
		"total_cents": ev.TotalCents,
		"occurred_at": ev.OccurredAt,
		"order_id":   orderID,
	}
	if ev.UserHash != 0 {
		doc["user_hash"] = ev.UserHash
	}
	if ev.TenantPartition != "" {
		doc["tenant_partition"] = ev.TenantPartition
	}
	if _, err := coll.InsertOne(wctx, doc); err != nil {
		return err
	}
	return nil
}

func tenantNameFor(idx int) string {
	if idx < 0 {
		idx = -idx
	}
	return "tenant-" + intToStr(idx+1)
}

func intToStr(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	if i < 0 {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	return string(buf[pos:])
}

func loadConfig() config {
	tenantCount := svchelp.EnvIntOrDefault("TENANT_COUNT", 64)
	if tenantCount <= 1 {
		tenantCount = 64
	}
	zipfS := svchelp.EnvFloatOrDefault("ZIPF_S", 1.07)
	if math.IsNaN(zipfS) || zipfS <= 1.0 {
		zipfS = 1.07
	}
	stratStr := svchelp.EnvOrDefault("SHARD_KEY", "candidate")
	strat, err := shardkey.Parse(stratStr, readFixedFallback())
	if err != nil {
		log.Fatalf("loadgen: SHARD_KEY: %v", err)
	}
	cfg := config{
		port:              svchelp.EnvOrDefault("PORT", "9000"),
		postgresDSN:       svchelp.EnvOrDefault("POSTGRES_DSN", "postgres://hlsa:hlsa@postgres:5432/hlsa?sslmode=disable"),
		mongoHosts:        svchelp.EnvOrDefault("MONGO_HOSTS", "mongos-1:27017,mongos-2:27017"),
		mongoDB:           svchelp.EnvOrDefault("MONGO_DB", "lab43"),
		faultURL:          svchelp.EnvOrDefault("FAULT_INJECTOR_URL", "http://fault-injector:9000"),
		hotSlot:           svchelp.EnvOrDefault("HOT_SLOT", "hot"),
		defaultStrategy:   strat,
		tenantCount:       tenantCount,
		zipfS:             zipfS,
		hashSuffixBuckets: svchelp.EnvIntOrDefault("HASH_SUFFIX_BUCKETS", 16),
	}
	return cfg
}

// readFixedFallback reads .candidate written by `make apply-fix` so the
// loadgen knows what `SHARD_KEY=fixed` resolves to. Returns "" if no
// fix has been applied yet.
func readFixedFallback() string {
	for _, p := range []string{"/lab/.candidate", ".candidate"} {
		b, err := os.ReadFile(p)
		if err == nil {
			return string(bytesTrim(b))
		}
	}
	return ""
}

func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == ' ' || b[len(b)-1] == '\r' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}
