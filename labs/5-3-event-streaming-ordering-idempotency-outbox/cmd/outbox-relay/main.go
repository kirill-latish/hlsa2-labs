// outbox-relay is the Debezium-style relay for this lab, implemented
// as a Go poller (see README "Why a Go relay instead of Debezium").
// It tails events_outbox in commit (id) order, publishes the committed
// rows to Redpanda keyed by aggregate_id, and stamps published_at in
// the SAME transaction as the fetch -> a crash mid-publish simply
// replays the row, which is the at-least-once delivery the idempotent
// consumer is built to absorb.
//
// HTTP API:
//
//	GET /healthz
//	GET /status   -> {"backlog": N, "lag_age_seconds": S, "published": P}
//	GET /metrics
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/hlsa2-labs/lab5-3/internal/metrics"
	"github.com/hlsa2-labs/lab5-3/internal/outbox"
)

type relay struct {
	pool      *pgxpool.Pool
	cli       *kgo.Client
	mx        *metrics.Relay
	topic     string
	published int64
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v <= 0 {
		return def
	}
	return v
}

func main() {
	port := envOrDefault("PORT", "9102")
	brokers := envOrDefault("REDPANDA_BROKERS", "redpanda:9092")
	topic := envOrDefault("EVENTS_TOPIC", "order-events")
	dsn := envOrDefault("DATABASE_URL", "postgres://lab53:lab53@postgres:5432/lab53?sslmode=disable")
	pollMS := envInt("POLL_INTERVAL_MS", 100)

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
		kgo.ProducerLinger(10*time.Millisecond),
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

	r := &relay{pool: pool, cli: cli, mx: metrics.NewRelay(), topic: topic}

	ctx, cancel := context.WithCancel(context.Background())
	go r.run(ctx, time.Duration(pollMS)*time.Millisecond)
	go r.sampleBacklog(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("/metrics", metrics.Handler())
	mux.HandleFunc("/status", r.handleStatus)
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("outbox-relay listening on :%s -> %s topic=%s poll=%dms", port, brokers, topic, pollMS)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	cancel()
	shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShut()
	_ = srv.Shutdown(shutCtx)
}

func (r *relay) handleStatus(w http.ResponseWriter, req *http.Request) {
	backlog, age, err := outbox.Backlog(req.Context(), r.pool)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"backlog":         backlog,
		"lag_age_seconds": age,
		"published":       atomic.LoadInt64(&r.published),
	})
}

func (r *relay) run(ctx context.Context, poll time.Duration) {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := r.drain(ctx)
			if err != nil {
				log.Printf("outbox-relay: %v", err)
				r.mx.PublishErrors.Inc()
				continue
			}
			if n > 0 {
				atomic.AddInt64(&r.published, int64(n))
				r.mx.Published.Add(float64(n))
			}
		}
	}
}

func (r *relay) drain(ctx context.Context) (int, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := outbox.FetchUnpublished(ctx, tx, 200)
	if err != nil {
		return 0, fmt.Errorf("fetch: %w", err)
	}
	if len(rows) == 0 {
		return 0, tx.Commit(ctx)
	}

	// Produce in id (commit) order, keyed by aggregate_id so per-entity
	// ordering survives the relay. ProduceSync preserves submission
	// order so the wire order matches the commit order.
	recs := make([]*kgo.Record, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, u := range rows {
		recs = append(recs, &kgo.Record{
			Topic: r.topic,
			Key:   []byte(u.Key),
			Value: u.Payload,
			Headers: []kgo.RecordHeader{
				{Key: "event_id", Value: []byte(u.EventID)},
				{Key: "event_type", Value: []byte(u.Type)},
				{Key: "source", Value: []byte("outbox-relay")},
			},
		})
		ids = append(ids, u.RowID)
	}
	produceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.cli.ProduceSync(produceCtx, recs...).FirstErr(); err != nil {
		return 0, fmt.Errorf("produce: %w", err)
	}

	if err := outbox.MarkPublished(ctx, tx, ids); err != nil {
		return 0, fmt.Errorf("mark published: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(rows), nil
}

func (r *relay) sampleBacklog(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			backlog, age, err := outbox.Backlog(ctx, r.pool)
			if err != nil {
				continue
			}
			r.mx.OutboxBacklog.Set(float64(backlog))
			r.mx.RelayLagAge.Set(age)
		}
	}
}
