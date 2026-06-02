// outbox-relay tails the events_outbox table in every per-service
// Postgres and publishes the unpublished rows to Redpanda. The
// publish + UPDATE published_at are kept in one transaction so the
// relay is at-least-once: a crash mid-publish replays the event,
// which is exactly the "at least once" delivery model the lab makes
// the consumer prove it survives via idempotency.
package main

import (
	"context"
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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/hlsa2-labs/lab4-2/internal/metrics"
	"github.com/hlsa2-labs/lab4-2/internal/outbox"
	"github.com/hlsa2-labs/lab4-2/internal/svchelp"
)

func main() {
	port := svchelp.EnvOrDefault("PORT", "9102")
	pollMS := envInt("POLL_INTERVAL_MS", 100)
	brokers := svchelp.EnvOrDefault("REDPANDA_BROKERS", "redpanda:9092")
	topic := svchelp.EnvOrDefault("EVENTS_TOPIC", "events")

	dsns := map[string]string{
		"payment":   svchelp.EnvOrDefault("PAYMENT_DSN", "postgres://payment:payment@payment-pg:5432/payment?sslmode=disable"),
		"inventory": svchelp.EnvOrDefault("INVENTORY_DSN", "postgres://inventory:inventory@inventory-pg:5432/inventory?sslmode=disable"),
		"shipping":  svchelp.EnvOrDefault("SHIPPING_DSN", "postgres://shipping:shipping@shipping-pg:5432/shipping?sslmode=disable"),
	}

	pools := make(map[string]*pgxpool.Pool, len(dsns))
	for name, dsn := range dsns {
		p, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			log.Fatalf("pool %s: %v", name, err)
		}
		// Wait until the DB is ready (services are racing on startup).
		for i := 0; i < 60; i++ {
			if err := p.Ping(context.Background()); err == nil {
				break
			}
			time.Sleep(time.Second)
		}
		pools[name] = p
	}

	cli, err := kgo.NewClient(
		kgo.SeedBrokers(brokers),
		kgo.ProducerLinger(20*time.Millisecond),
		kgo.RetryTimeout(5*time.Second),
	)
	if err != nil {
		log.Fatalf("kafka client: %v", err)
	}
	defer cli.Close()

	// Wait for the broker to accept connections.
	for i := 0; i < 60; i++ {
		if err := cli.Ping(context.Background()); err == nil {
			break
		}
		time.Sleep(time.Second)
	}

	mx := newRelayMetrics()

	// One worker per source DB.
	ctx, cancel := context.WithCancel(context.Background())
	for name, p := range pools {
		go runWorker(ctx, name, p, cli, topic, time.Duration(pollMS)*time.Millisecond, mx)
	}

	// Tiny HTTP for /metrics + /healthz so prometheus can scrape us.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("/metrics", metrics.Handler())
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("outbox-relay listening on :%s -> %s topic=%s", port, brokers, topic)
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

func runWorker(ctx context.Context, source string, pool *pgxpool.Pool, cli *kgo.Client, topic string, poll time.Duration, mx *relayMetrics) {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	log.Printf("outbox-relay worker[%s] starting (poll=%s)", source, poll)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := drain(ctx, source, pool, cli, topic)
			if err != nil {
				log.Printf("outbox-relay[%s]: %v", source, err)
				mx.errors.WithLabelValues(source).Inc()
				continue
			}
			if n > 0 {
				mx.published.WithLabelValues(source).Add(float64(n))
			}
		}
	}
}

func drain(ctx context.Context, source string, pool *pgxpool.Pool, cli *kgo.Client, topic string) (int, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
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

	// Produce to Redpanda. Block until the broker has acked the batch
	// so we can mark the rows published in the same transaction; if
	// produce fails, the rollback re-opens the rows for the next poll.
	produceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var produceErr error
	pending := int32(int32(len(rows)))
	done := make(chan struct{})
	for _, u := range rows {
		rec := &kgo.Record{
			Topic: topic,
			Key:   []byte(u.AggregateID),
			Value: u.Payload,
			Headers: []kgo.RecordHeader{
				{Key: "event_id", Value: []byte(u.EventID)},
				{Key: "event_type", Value: []byte(u.Type)},
				{Key: "source", Value: []byte(source)},
			},
		}
		cli.Produce(produceCtx, rec, func(_ *kgo.Record, err error) {
			if err != nil && produceErr == nil {
				produceErr = err
			}
			if atomic.AddInt32(&pending, -1) == 0 {
				close(done)
			}
		})
	}
	select {
	case <-done:
	case <-produceCtx.Done():
		return 0, fmt.Errorf("produce timed out: %w", produceCtx.Err())
	}
	if produceErr != nil {
		return 0, fmt.Errorf("produce: %w", produceErr)
	}

	ids := make([]int64, 0, len(rows))
	for _, u := range rows {
		ids = append(ids, u.RowID)
	}
	if err := outbox.MarkPublished(ctx, tx, ids); err != nil {
		return 0, fmt.Errorf("mark published: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(rows), nil
}

func envInt(key string, def int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v <= 0 {
		return def
	}
	return v
}

type relayMetrics struct {
	published *prometheus.CounterVec
	errors    *prometheus.CounterVec
}

func newRelayMetrics() *relayMetrics {
	m := &relayMetrics{
		published: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_published_total", Help: "Outbox events successfully published to Redpanda.",
		}, []string{"source"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_publish_errors_total", Help: "Outbox publish failures.",
		}, []string{"source"}),
	}
	metrics.MustRegister(m.published, m.errors)
	return m
}
