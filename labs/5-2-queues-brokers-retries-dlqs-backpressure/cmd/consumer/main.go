// consumer reads from the RabbitMQ work queue, applies a simulated
// downstream Postgres write, and handles failures according to a
// runtime-flippable retry policy:
//
//	unbounded-retry    nack+requeue forever (the poison-message trap)
//	bounded-retry      retry with exp backoff+jitter, DLQ after MAX_RETRIES
//	classify-failures  permanent -> DLQ immediately; transient -> bounded retry
//	backpressure-signal bounded retry; backpressure is exerted at the
//	                    broker (bounded queue) and honored by the producer
//
// The mode + MAX_RETRIES are flipped at runtime via POST /admin/config
// (this is what `make apply-fix` calls), so the fleet recovers without
// recreating containers.
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
	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/hlsa2-labs/lab5-2/internal/metrics"
	"github.com/hlsa2-labs/lab5-2/internal/pipeline"
)

// Retry semantics modes.
const (
	ModeUnbounded    = "unbounded-retry"
	ModeBounded      = "bounded-retry"
	ModeClassify     = "classify-failures"
	ModeBackpressure = "backpressure-signal"
)

var knownModes = []string{ModeUnbounded, ModeBounded, ModeClassify, ModeBackpressure}

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

// config holds the runtime-flippable knobs.
type config struct {
	mu         sync.RWMutex
	mode       string
	maxRetries int
}

func (c *config) get() (string, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode, c.maxRetries
}

func (c *config) set(mode string, maxRetries int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = mode
	if maxRetries > 0 {
		c.maxRetries = maxRetries
	}
}

// consumer bundles everything a delivery handler needs.
type consumer struct {
	id   string
	cfg  *config
	mx   *metrics.Consumer
	pool *pgxpool.Pool

	// pubCh is a dedicated channel for republish (delayed retry) and
	// dead-lettering, kept separate from the consume channel.
	pubMu sync.Mutex
	pubCh *amqp.Channel

	baseLatency  time.Duration
	jitterMS     int
	recoverAfter int

	ctx context.Context

	processed int64
}

func main() {
	port := envOrDefault("PORT", "8081")
	consumerID := envOrDefault("CONSUMER_ID", "consumer")
	amqpURL := envOrDefault("RABBITMQ_URL", "amqp://guest:guest@rabbitmq:5672/")
	dsn := envOrDefault("DATABASE_URL", "postgres://lab52:lab52@postgres:5432/lab52?sslmode=disable")
	mode := envOrDefault("CONSUMER_MODE", ModeUnbounded)
	maxRetries := envInt("MAX_RETRIES", 5)
	maxLen := envInt("QUEUE_MAX_LEN", 50000)
	baseLatencyMS := envInt("DOWNSTREAM_BASE_LATENCY_MS", 5)
	jitterMS := envInt("DOWNSTREAM_JITTER_MS", 10)
	recoverAfter := envInt("TRANSIENT_RECOVER_AFTER", 2)

	if !validMode(mode) {
		log.Fatalf("CONSUMER_MODE must be one of %v, got %q", knownModes, mode)
	}

	mx := metrics.NewConsumer(consumerID)

	// Postgres downstream.
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("pg pool: %v", err)
	}
	defer pool.Close()
	if err := waitPG(pool); err != nil {
		log.Fatalf("pg not ready: %v", err)
	}
	if err := ensureTable(pool); err != nil {
		log.Fatalf("create table: %v", err)
	}

	conn, err := pipeline.Connect(amqpURL, 60)
	if err != nil {
		log.Fatalf("rabbitmq connect: %v", err)
	}
	defer conn.Close()

	consumeCh, err := conn.Channel()
	if err != nil {
		log.Fatalf("consume channel: %v", err)
	}
	if err := pipeline.DeclareTopology(consumeCh, maxLen); err != nil {
		log.Fatalf("declare topology: %v", err)
	}
	// One unacked message at a time so a wedged poison message visibly
	// occupies this consumer's single slot under unbounded retries.
	if err := consumeCh.Qos(1, 0, false); err != nil {
		log.Fatalf("qos: %v", err)
	}
	pubCh, err := conn.Channel()
	if err != nil {
		log.Fatalf("publish channel: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	cfg := &config{mode: mode, maxRetries: maxRetries}
	c := &consumer{
		id:           consumerID,
		cfg:          cfg,
		mx:           mx,
		pool:         pool,
		pubCh:        pubCh,
		baseLatency:  time.Duration(baseLatencyMS) * time.Millisecond,
		jitterMS:     jitterMS,
		recoverAfter: recoverAfter,
		ctx:          ctx,
	}
	c.publishMode(mode)

	deliveries, err := consumeCh.Consume(pipeline.WorkQueue, consumerID, false, false, false, false, nil)
	if err != nil {
		log.Fatalf("consume: %v", err)
	}
	go func() {
		for d := range deliveries {
			c.handle(d)
		}
	}()

	// Control surface.
	httpMetrics := metrics.NewHTTPMetrics(consumerID)
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", metrics.Handler())
	r.Get("/state", func(w http.ResponseWriter, _ *http.Request) {
		m, mr := cfg.get()
		writeJSON(w, map[string]any{
			"consumer":    consumerID,
			"mode":        m,
			"max_retries": mr,
			"processed":   atomic.LoadInt64(&c.processed),
		})
	})
	r.Post("/admin/config", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode       string `json:"mode"`
			MaxRetries int    `json:"max_retries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.Mode != "" && !validMode(body.Mode) {
			http.Error(w, "invalid mode "+body.Mode, http.StatusBadRequest)
			return
		}
		curMode, _ := cfg.get()
		newMode := curMode
		if body.Mode != "" {
			newMode = body.Mode
		}
		cfg.set(newMode, body.MaxRetries)
		c.publishMode(newMode)
		m, mr := cfg.get()
		log.Printf("admin/config: mode=%s max_retries=%d", m, mr)
		writeJSON(w, map[string]any{"mode": m, "max_retries": mr})
	})

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("consumer[%s] listening on :%s mode=%s max_retries=%d", consumerID, port, mode, maxRetries)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	cancel()
	shutdownCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShut()
	_ = srv.Shutdown(shutdownCtx)
}

func (c *consumer) handle(d amqp.Delivery) {
	start := time.Now()
	mode, maxRetries := c.cfg.get()
	attempt := pipeline.HeaderInt(d.Headers, pipeline.HeaderRetryCount)

	var msg pipeline.Message
	parseErr := json.Unmarshal(d.Body, &msg)
	if !msg.EnqueuedAt.IsZero() {
		// Lag - time: age of the message we just dequeued.
		c.mx.OldestUnprocessed.Set(time.Since(msg.EnqueuedAt).Seconds())
	}

	ok, permanent := false, true
	if parseErr == nil {
		ok, permanent = c.evaluate(msg, attempt)
	}
	c.mx.ProcessingSeconds.Observe(time.Since(start).Seconds())

	if ok {
		_ = d.Ack(false)
		c.mx.Acked.Inc()
		atomic.AddInt64(&c.processed, 1)
		return
	}

	switch mode {
	case ModeUnbounded:
		// No bound: keep requeueing. One poison message can occupy a
		// consumer slot forever and starve healthy messages.
		c.mx.Retries.Inc()
		_ = d.Nack(false, true)
	case ModeClassify:
		if permanent {
			c.toDLQ(d, msg)
			return
		}
		c.boundedRetryOrDLQ(d, msg, attempt, maxRetries)
	default: // bounded-retry, backpressure-signal
		c.boundedRetryOrDLQ(d, msg, attempt, maxRetries)
	}
}

// boundedRetryOrDLQ schedules a delayed republish (freeing the consumer
// slot immediately) until MAX_RETRIES is hit, then dead-letters.
func (c *consumer) boundedRetryOrDLQ(d amqp.Delivery, msg pipeline.Message, attempt, maxRetries int) {
	if attempt < maxRetries {
		c.mx.Retries.Inc()
		delay := backoff(attempt)
		go func() {
			select {
			case <-time.After(delay):
			case <-c.ctx.Done():
				return
			}
			c.republish(msg, attempt+1)
		}()
		_ = d.Ack(false)
		return
	}
	c.toDLQ(d, msg)
}

func (c *consumer) toDLQ(d amqp.Delivery, msg pipeline.Message) {
	c.publishDLQ(msg)
	c.mx.DLQ.Inc()
	_ = d.Ack(false)
}

// evaluate simulates the downstream outcome for a message. Returns
// (processedOK, permanentFailure).
func (c *consumer) evaluate(msg pipeline.Message, attempt int) (bool, bool) {
	switch msg.Type {
	case pipeline.TypeNormal:
		c.downstreamWrite(msg, attempt)
		return true, false
	case pipeline.TypePoison, pipeline.TypePermanent:
		return false, true
	case pipeline.TypeTransient:
		if attempt >= c.recoverAfter {
			c.downstreamWrite(msg, attempt)
			return true, false
		}
		return false, false
	default:
		return false, true
	}
}

// downstreamWrite simulates query latency then writes to Postgres.
func (c *consumer) downstreamWrite(msg pipeline.Message, attempt int) {
	sleep := c.baseLatency
	if c.jitterMS > 0 {
		sleep += time.Duration(rand.Intn(c.jitterMS)) * time.Millisecond
	}
	time.Sleep(sleep)
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	_, _ = c.pool.Exec(ctx,
		`INSERT INTO processed_messages (msg_id, msg_type, consumer_id, attempt)
		 VALUES ($1,$2,$3,$4) ON CONFLICT (msg_id) DO NOTHING`,
		msg.ID, string(msg.Type), c.id, attempt)
}

func (c *consumer) republish(msg pipeline.Message, nextAttempt int) {
	body, _ := json.Marshal(msg)
	c.pubMu.Lock()
	defer c.pubMu.Unlock()
	_ = c.pubCh.PublishWithContext(c.ctx, pipeline.WorkExchange, pipeline.WorkRoutingKey, false, false, amqp.Publishing{
		ContentType: "application/json",
		MessageId:   msg.ID,
		Timestamp:   msg.EnqueuedAt,
		Body:        body,
		Headers: amqp.Table{
			pipeline.HeaderRetryCount: int32(nextAttempt),
			pipeline.HeaderType:       string(msg.Type),
		},
	})
}

func (c *consumer) publishDLQ(msg pipeline.Message) {
	body, _ := json.Marshal(msg)
	c.pubMu.Lock()
	defer c.pubMu.Unlock()
	_ = c.pubCh.PublishWithContext(c.ctx, pipeline.DLXExchange, "", false, false, amqp.Publishing{
		ContentType: "application/json",
		MessageId:   msg.ID,
		Body:        body,
		Headers:     amqp.Table{pipeline.HeaderType: string(msg.Type)},
	})
}

func (c *consumer) publishMode(mode string) {
	for _, m := range knownModes {
		v := 0.0
		if m == mode {
			v = 1
		}
		c.mx.Mode.WithLabelValues(m).Set(v)
	}
}

// backoff returns exponential backoff with ±20% jitter, capped at 16s.
// attempt 0 -> ~1s, 1 -> ~2s, 2 -> ~4s, 3 -> ~8s, >=4 -> ~16s.
func backoff(attempt int) time.Duration {
	base := time.Second << uint(attempt)
	if base > 16*time.Second {
		base = 16 * time.Second
	}
	jitter := 1.0 + (rand.Float64()*0.4 - 0.2)
	return time.Duration(float64(base) * jitter)
}

func ensureTable(pool *pgxpool.Pool) error {
	_, err := pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS processed_messages (
			msg_id       TEXT PRIMARY KEY,
			msg_type     TEXT NOT NULL,
			consumer_id  TEXT NOT NULL,
			attempt      INT  NOT NULL,
			processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	return err
}

func waitPG(pool *pgxpool.Pool) error {
	var lastErr error
	for i := 0; i < 60; i++ {
		if err := pool.Ping(context.Background()); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	return lastErr
}

func validMode(m string) bool {
	for _, k := range knownModes {
		if k == m {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
