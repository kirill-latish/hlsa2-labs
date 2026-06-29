// producer models an order service that emits events. It is the one
// knob-box the experiments drive:
//
//   - partition-key strategy (entity | wrong) decides ordering
//   - duplicate injection (rate) exercises the consumer's dedup
//   - publish mode (direct | naive-dualwrite | outbox) decides whether
//     the DB write and the publish are atomic
//   - a one-shot crash-between-writes injection drops the publish after
//     the DB commit, manufacturing the dual-write orphan
//
// Control API:
//
//	POST /start        {"rate_eps":200,"duration_s":60,"label":"baseline"}
//	POST /stop
//	GET  /state
//	GET  /summary
//	POST /admin/config {"key_strategy":"entity","duplicate_rate":0.2,"publish_mode":"direct"}
//	POST /admin/arm-crash {"after":10}
//	GET  /healthz
//	GET  /metrics
package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/hlsa2-labs/lab5-3/internal/events"
	"github.com/hlsa2-labs/lab5-3/internal/metrics"
	"github.com/hlsa2-labs/lab5-3/internal/outbox"
)

var paymentMethods = []string{"card", "paypal", "applepay", "bank"}

type producer struct {
	cli   *kgo.Client
	pool  *pgxpool.Pool
	mx    *metrics.Producer
	topic string

	mu            sync.Mutex
	keyStrategy   string
	duplicateRate float64
	publishMode   string
	numOrders     int

	// crashAfter > 0 arms a one-shot crash-between-writes: after that
	// many committed dual-writes, the process exits before the publish.
	crashAfter    int
	dualWriteSeen int

	// run state
	running   bool
	cancel    context.CancelFunc
	label     string
	startedAt time.Time
	endsAt    time.Time
	rateEPS   int

	produced   int64
	unique     int64
	duplicates int64

	seqMu     sync.Mutex
	perOrder  map[string]int64
	nextOrder int64
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
	brokers := envOrDefault("REDPANDA_BROKERS", "redpanda:9092")
	topic := envOrDefault("EVENTS_TOPIC", "order-events")
	dsn := envOrDefault("DATABASE_URL", "postgres://lab53:lab53@postgres:5432/lab53?sslmode=disable")

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
		kgo.ProducerLinger(5*time.Millisecond),
		kgo.RetryTimeout(5*time.Second),
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

	p := &producer{
		cli:           cli,
		pool:          pool,
		mx:            metrics.NewProducer(),
		topic:         topic,
		keyStrategy:   envOrDefault("KEY_STRATEGY", "entity"),
		duplicateRate: 0,
		publishMode:   envOrDefault("PUBLISH_MODE", "direct"),
		numOrders:     envInt("NUM_ORDERS", 500),
		perOrder:      make(map[string]int64),
	}

	httpMetrics := metrics.NewHTTPMetrics("producer")
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", metrics.Handler())
	r.Get("/state", p.handleState)
	r.Get("/summary", p.handleState)
	r.Post("/stop", p.handleStop)
	r.Post("/start", p.handleStart)
	r.Post("/admin/config", p.handleConfig)
	r.Post("/admin/arm-crash", p.handleArmCrash)

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("producer listening on :%s broker=%s topic=%s mode=%s key=%s",
			port, brokers, topic, p.publishMode, p.keyStrategy)
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

func (p *producer) snapshot() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]any{
		"running":        p.running,
		"label":          p.label,
		"key_strategy":   p.keyStrategy,
		"duplicate_rate": p.duplicateRate,
		"publish_mode":   p.publishMode,
		"num_orders":     p.numOrders,
		"crash_after":    p.crashAfter,
		"started_at":     p.startedAt.UTC().Format(time.RFC3339Nano),
		"ends_at":        p.endsAt.UTC().Format(time.RFC3339Nano),
		"rate_eps":       p.rateEPS,
		"produced":       atomic.LoadInt64(&p.produced),
		"unique":         atomic.LoadInt64(&p.unique),
		"duplicates":     atomic.LoadInt64(&p.duplicates),
	}
}

func (p *producer) handleState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, p.snapshot())
}

func (p *producer) handleStop(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.running = false
	p.mu.Unlock()
	_, _ = w.Write([]byte("stopped"))
}

type startReq struct {
	RateEPS   int    `json:"rate_eps"`
	DurationS int    `json:"duration_s"`
	Label     string `json:"label"`
}

func (p *producer) handleStart(w http.ResponseWriter, r *http.Request) {
	var req startReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.RateEPS <= 0 {
		req.RateEPS = 200
	}
	if req.DurationS <= 0 {
		req.DurationS = 60
	}
	if req.Label == "" {
		req.Label = "run"
	}

	p.mu.Lock()
	if p.running && p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.running = true
	p.cancel = cancel
	p.label = req.Label
	p.startedAt = time.Now()
	p.endsAt = time.Now().Add(time.Duration(req.DurationS) * time.Second)
	p.rateEPS = req.RateEPS
	atomic.StoreInt64(&p.produced, 0)
	atomic.StoreInt64(&p.unique, 0)
	atomic.StoreInt64(&p.duplicates, 0)
	p.mu.Unlock()

	go p.drive(ctx, req)
	writeJSON(w, p.snapshot())
}

type configReq struct {
	KeyStrategy   *string  `json:"key_strategy"`
	DuplicateRate *float64 `json:"duplicate_rate"`
	PublishMode   *string  `json:"publish_mode"`
}

func (p *producer) handleConfig(w http.ResponseWriter, r *http.Request) {
	var req configReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	if req.KeyStrategy != nil {
		p.keyStrategy = *req.KeyStrategy
	}
	if req.DuplicateRate != nil {
		p.duplicateRate = *req.DuplicateRate
	}
	if req.PublishMode != nil {
		p.publishMode = *req.PublishMode
	}
	p.mu.Unlock()
	writeJSON(w, p.snapshot())
}

type armReq struct {
	After int `json:"after"`
}

func (p *producer) handleArmCrash(w http.ResponseWriter, r *http.Request) {
	var req armReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.After <= 0 {
		req.After = 10
	}
	p.mu.Lock()
	p.crashAfter = req.After
	p.dualWriteSeen = 0
	p.mu.Unlock()
	writeJSON(w, map[string]any{"armed": true, "after": req.After})
}

func (p *producer) drive(ctx context.Context, req startReq) {
	tick := time.Duration(float64(time.Second) / float64(req.RateEPS))
	if tick <= 0 {
		tick = time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	deadline := time.Now().Add(time.Duration(req.DurationS) * time.Second)

	for {
		select {
		case <-ctx.Done():
			p.finish()
			return
		case now := <-t.C:
			if now.After(deadline) {
				p.finish()
				return
			}
			p.produceOne(ctx)
		}
	}
}

func (p *producer) finish() {
	p.mu.Lock()
	p.running = false
	p.mu.Unlock()
}

func (p *producer) nextSeq(orderID string) int64 {
	p.seqMu.Lock()
	defer p.seqMu.Unlock()
	p.perOrder[orderID]++
	return p.perOrder[orderID]
}

func (p *producer) produceOne(ctx context.Context) {
	p.mu.Lock()
	mode := p.publishMode
	key := p.keyStrategy
	dupRate := p.duplicateRate
	numOrders := p.numOrders
	p.mu.Unlock()

	var e events.Event
	if mode == "direct" {
		// Reuse a fixed pool of orders so each entity accumulates a
		// sequence of events; that is what the ordering experiment needs.
		orderID := "order-" + strconv.Itoa(rand.Intn(numOrders))
		seq := p.nextSeq(orderID)
		typ := "updated"
		if seq == 1 {
			typ = "created"
		}
		e = events.Event{
			EventID:       uuid.NewString(),
			OrderID:       orderID,
			PaymentMethod: paymentMethods[rand.Intn(len(paymentMethods))],
			Seq:           seq,
			Type:          typ,
			Amount:        int64(1 + rand.Intn(100)),
		}
		p.publishDirect(ctx, e, key, dupRate)
		return
	}

	// Dual-write / outbox modes use a brand-new order per event so a
	// lost publish is an unambiguous orphan.
	n := atomic.AddInt64(&p.nextOrder, 1)
	orderID := "dw-order-" + strconv.FormatInt(n, 10)
	e = events.Event{
		EventID:       uuid.NewString(),
		OrderID:       orderID,
		PaymentMethod: paymentMethods[rand.Intn(len(paymentMethods))],
		Seq:           1,
		Type:          "created",
		Amount:        int64(1 + rand.Intn(100)),
	}

	switch mode {
	case "naive-dualwrite":
		p.publishNaiveDualWrite(ctx, e)
	case "outbox":
		p.publishOutbox(ctx, e)
	default:
		p.publishDirect(ctx, e, key, dupRate)
	}
}

func (p *producer) publishDirect(ctx context.Context, e events.Event, key string, dupRate float64) {
	p.send(ctx, e, key)
	atomic.AddInt64(&p.produced, 1)
	atomic.AddInt64(&p.unique, 1)
	p.mx.EventsProduced.WithLabelValues(key, e.Type, "direct").Inc()

	if dupRate > 0 && rand.Float64() < dupRate {
		// Re-publish the identical event_id: an injected duplicate.
		p.send(ctx, e, key)
		atomic.AddInt64(&p.produced, 1)
		atomic.AddInt64(&p.duplicates, 1)
		p.mx.DuplicatesEmitted.Inc()
		p.mx.EventsProduced.WithLabelValues(key, e.Type, "direct").Inc()
	}
}

func (p *producer) publishNaiveDualWrite(ctx context.Context, e events.Event) {
	if err := p.writeOrder(ctx, e, false); err != nil {
		log.Printf("producer: naive-dualwrite db: %v", err)
		return
	}
	p.mx.DualWriteCommitted.WithLabelValues("naive-dualwrite").Inc()

	if p.shouldCrash() {
		log.Printf("producer: CRASH-between-writes (naive-dualwrite) after DB commit, before publish: order=%s", e.OrderID)
		os.Exit(1)
	}

	p.send(ctx, e, "entity")
	atomic.AddInt64(&p.produced, 1)
	atomic.AddInt64(&p.unique, 1)
	p.mx.EventsProduced.WithLabelValues("entity", e.Type, "naive-dualwrite").Inc()
}

func (p *producer) publishOutbox(ctx context.Context, e events.Event) {
	if err := p.writeOrder(ctx, e, true); err != nil {
		log.Printf("producer: outbox tx: %v", err)
		return
	}
	p.mx.DualWriteCommitted.WithLabelValues("outbox").Inc()

	if p.shouldCrash() {
		// Both rows already committed in one tx; the relay will publish
		// the outbox row, so this crash produces no orphan.
		log.Printf("producer: CRASH-between-writes (outbox) after atomic commit: order=%s", e.OrderID)
		os.Exit(1)
	}
	// No direct publish: the outbox-relay ships the committed row.
	atomic.AddInt64(&p.produced, 1)
	atomic.AddInt64(&p.unique, 1)
	p.mx.EventsProduced.WithLabelValues("entity", e.Type, "outbox").Inc()
}

// writeOrder upserts the business row, and (when withOutbox) inserts
// the matching outbox row in the SAME transaction.
func (p *producer) writeOrder(ctx context.Context, e events.Event, withOutbox bool) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO orders (order_id, last_seq, status, amount)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (order_id) DO UPDATE SET
		   last_seq = EXCLUDED.last_seq, status = EXCLUDED.status,
		   amount = EXCLUDED.amount, updated_at = now()`,
		e.OrderID, e.Seq, e.Type, e.Amount,
	); err != nil {
		return err
	}
	if withOutbox {
		if err := outbox.Insert(ctx, tx, e); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (p *producer) shouldCrash() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.crashAfter <= 0 {
		return false
	}
	p.dualWriteSeen++
	return p.dualWriteSeen >= p.crashAfter
}

func (p *producer) send(ctx context.Context, e events.Event, key string) {
	start := time.Now()
	rec := e.ToRecord(p.topic, key)
	res := p.cli.ProduceSync(ctx, rec)
	if err := res.FirstErr(); err != nil {
		log.Printf("producer: publish: %v", err)
		return
	}
	p.mx.PublishLatency.Observe(time.Since(start).Seconds())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
