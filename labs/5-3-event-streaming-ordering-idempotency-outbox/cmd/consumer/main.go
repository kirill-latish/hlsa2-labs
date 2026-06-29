// consumer reads order-events in a single consumer group (three
// instances share the group) and applies each event through
// internal/consumer.Apply. It detects ordering violations via the
// per-entity sequence number, dedups by event_id when in idempotent
// mode, maintains the projection read-model, counts external side
// effects, and can crash mid-batch on demand to prove the dedup
// survives redelivery.
//
// Control API:
//
//	POST /admin/mode        {"mode":"idempotent-consumer"}
//	POST /admin/replay-mode {"mode":"rebuild-only"}
//	POST /admin/crash       {"after":0}   (arm one-shot mid-batch crash)
//	GET  /state
//	GET  /healthz
//	GET  /metrics
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	consumerpkg "github.com/hlsa2-labs/lab5-3/internal/consumer"
	"github.com/hlsa2-labs/lab5-3/internal/events"
	"github.com/hlsa2-labs/lab5-3/internal/metrics"
)

type app struct {
	pool  *pgxpool.Pool
	cli   *kgo.Client
	adm   *kadm.Client
	mx    *metrics.Consumer
	topic string
	group string

	mu     sync.Mutex
	mode   consumerpkg.Mode
	replay consumerpkg.ReplayMode

	crashArmed   int32
	lastRecordNs int64
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func main() {
	port := envOrDefault("PORT", "9103")
	brokers := envOrDefault("REDPANDA_BROKERS", "redpanda:9092")
	topic := envOrDefault("EVENTS_TOPIC", "order-events")
	group := envOrDefault("CONSUMER_GROUP", "lab53-consumers")
	instance := envOrDefault("INSTANCE", "consumer")
	dsn := envOrDefault("DATABASE_URL", "postgres://lab53:lab53@postgres:5432/lab53?sslmode=disable")
	enableLag := envBool("ENABLE_LAG_SAMPLER", false)

	mode := consumerpkg.Mode(envOrDefault("CONSUMER_MODE", string(consumerpkg.ModeIdempotent)))
	if !mode.Valid() {
		log.Fatalf("CONSUMER_MODE must be naive-consumer|idempotent-consumer, got %q", mode)
	}
	replay := consumerpkg.ReplayMode(envOrDefault("REPLAY_MODE", string(consumerpkg.ReplayOff)))
	if !replay.Valid() {
		log.Fatalf("REPLAY_MODE must be off|rebuild-only|reprocess, got %q", replay)
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	for i := 0; i < 60; i++ {
		if err := pool.Ping(context.Background()); err == nil {
			break
		}
		time.Sleep(time.Second)
	}

	cli, err := kgo.NewClient(
		kgo.SeedBrokers(brokers),
		kgo.ConsumeTopics(topic),
		kgo.ConsumerGroup(group),
		kgo.DisableAutoCommit(),
		// A group with no committed offset (fresh, or after `rpk group
		// delete` during replay) reads from the start of the log. This
		// is what lets `make replay-rebuild FROM=earliest` rewind.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		log.Fatalf("kafka client: %v", err)
	}
	defer cli.Close()
	for i := 0; i < 60; i++ {
		if err := cli.Ping(context.Background()); err == nil {
			break
		}
		time.Sleep(time.Second)
	}

	a := &app{
		pool:   pool,
		cli:    cli,
		adm:    kadm.NewClient(cli),
		mx:     metrics.NewConsumer(instance),
		topic:  topic,
		group:  group,
		mode:   mode,
		replay: replay,
	}
	a.reflectModeMetric()

	httpMetrics := metrics.NewHTTPMetrics("consumer")
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", metrics.Handler())
	r.Get("/state", a.handleState)
	r.Post("/admin/mode", a.handleMode)
	r.Post("/admin/replay-mode", a.handleReplayMode)
	r.Post("/admin/crash", a.handleCrash)

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("consumer[%s] listening on :%s mode=%s replay=%s group=%s", instance, port, mode, replay, group)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go a.runConsumer(ctx)
	if enableLag {
		go a.sampleLag(ctx)
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	cancel()
	shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShut()
	_ = srv.Shutdown(shutCtx)
}

func (a *app) getModes() (consumerpkg.Mode, consumerpkg.ReplayMode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode, a.replay
}

func (a *app) reflectModeMetric() {
	a.mu.Lock()
	mode, replay := a.mode, a.replay
	a.mu.Unlock()
	a.mx.Mode.WithLabelValues("idempotent").Set(boolToFloat(mode == consumerpkg.ModeIdempotent))
	a.mx.Mode.WithLabelValues("naive").Set(boolToFloat(mode == consumerpkg.ModeNaive))
	a.mx.Mode.WithLabelValues("replay_rebuild_only").Set(boolToFloat(replay == consumerpkg.ReplayRebuildOnly))
	a.mx.Mode.WithLabelValues("replay_reprocess").Set(boolToFloat(replay == consumerpkg.ReplayReprocess))
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (a *app) handleState(w http.ResponseWriter, _ *http.Request) {
	mode, replay := a.getModes()
	writeJSON(w, map[string]any{
		"mode":        mode,
		"replay_mode": replay,
		"crash_armed": atomic.LoadInt32(&a.crashArmed) == 1,
	})
}

func (a *app) handleMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m := consumerpkg.Mode(req.Mode)
	if !m.Valid() {
		http.Error(w, "invalid mode", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.mode = m
	a.mu.Unlock()
	a.reflectModeMetric()
	a.handleState(w, r)
}

func (a *app) handleReplayMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m := consumerpkg.ReplayMode(req.Mode)
	if !m.Valid() {
		http.Error(w, "invalid replay mode", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.replay = m
	a.mu.Unlock()
	a.reflectModeMetric()
	a.handleState(w, r)
}

func (a *app) handleCrash(w http.ResponseWriter, r *http.Request) {
	atomic.StoreInt32(&a.crashArmed, 1)
	writeJSON(w, map[string]any{"crash_armed": true})
}

func (a *app) runConsumer(ctx context.Context) {
	for {
		fetches := a.cli.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				if errors.Is(e.Err, context.Canceled) {
					return
				}
				log.Printf("consumer: fetch error topic=%s partition=%d: %v", e.Topic, e.Partition, e.Err)
			}
		}
		recs := fetches.Records()
		mode, replay := a.getModes()
		crashAt := -1
		if atomic.LoadInt32(&a.crashArmed) == 1 && len(recs) > 0 {
			// Crash midway through the batch BEFORE committing offsets so
			// the uncommitted tail is redelivered on restart.
			crashAt = len(recs) / 2
		}

		for i, rec := range recs {
			if crashAt >= 0 && i == crashAt {
				log.Printf("consumer: CRASH mid-batch after %d/%d records, before commit", i, len(recs))
				os.Exit(1)
			}
			a.applyRecord(ctx, rec, mode, replay)
			atomic.StoreInt64(&a.lastRecordNs, rec.Timestamp.UnixNano())
		}

		if err := a.cli.CommitUncommittedOffsets(ctx); err != nil {
			log.Printf("consumer: commit offsets: %v", err)
		}
	}
}

func (a *app) applyRecord(ctx context.Context, rec *kgo.Record, mode consumerpkg.Mode, replay consumerpkg.ReplayMode) {
	start := time.Now()
	e, err := events.FromRecord(rec)
	if err != nil {
		log.Printf("consumer: bad record: %v", err)
		return
	}
	res, err := consumerpkg.Apply(ctx, a.pool, mode, replay, e)
	if err != nil {
		log.Printf("consumer: apply event_id=%s err=%v", e.EventID, err)
		return
	}
	a.mx.EventsConsumed.Inc()
	a.mx.ProcessingLatency.Observe(time.Since(start).Seconds())
	if res.Duplicate {
		a.mx.DuplicateSuppressed.Inc()
	}
	if res.OrderingViolation {
		a.mx.OrderingViolations.Inc()
	}
	if res.SideEffectFired {
		a.mx.SideEffects.WithLabelValues("notify").Inc()
	}
}

// sampleLag periodically computes the whole-group lag (end offsets
// minus committed offsets) so the dashboard has a real backlog gauge.
// Only one instance should run this (ENABLE_LAG_SAMPLER) to avoid
// triple-counting the shared group's lag.
func (a *app) sampleLag(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ends, err := a.adm.ListEndOffsets(ctx, a.topic)
			if err != nil {
				continue
			}
			committed, _ := a.adm.FetchOffsets(ctx, a.group)
			var lag int64
			ends.Each(func(o kadm.ListedOffset) {
				var c int64
				if resp, ok := committed.Lookup(o.Topic, o.Partition); ok {
					c = resp.At
				}
				if d := o.Offset - c; d > 0 {
					lag += d
				}
			})
			a.mx.ConsumerLagCount.Set(float64(lag))
			if lag > 0 {
				last := atomic.LoadInt64(&a.lastRecordNs)
				if last > 0 {
					a.mx.ConsumerLagAge.Set(time.Since(time.Unix(0, last)).Seconds())
				}
			} else {
				a.mx.ConsumerLagAge.Set(0)
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
