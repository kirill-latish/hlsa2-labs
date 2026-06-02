// consumer reads from the events topic and applies side effects in
// either naive or idempotent mode. The replay test (`make replay`)
// resets offsets to 0 and re-runs everything; assert-idempotent then
// hashes the payment-pg state and checks naive != idempotent.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kgo"

	consumerpkg "github.com/hlsa2-labs/lab4-2/internal/consumer"
	"github.com/hlsa2-labs/lab4-2/internal/metrics"
	"github.com/hlsa2-labs/lab4-2/internal/svchelp"
)

func main() {
	port := svchelp.EnvOrDefault("PORT", "9103")
	brokers := svchelp.EnvOrDefault("REDPANDA_BROKERS", "redpanda:9092")
	topic := svchelp.EnvOrDefault("EVENTS_TOPIC", "events")
	group := svchelp.EnvOrDefault("CONSUMER_GROUP", "lab42-consumer")
	mode := strings.ToLower(svchelp.EnvOrDefault("CONSUMER_MODE", "idempotent"))
	dsn := svchelp.EnvOrDefault("DATABASE_URL", "postgres://payment:payment@payment-pg:5432/payment?sslmode=disable")

	if mode != string(consumerpkg.ModeIdempotent) && mode != string(consumerpkg.ModeNaive) {
		log.Fatalf("CONSUMER_MODE must be 'idempotent' or 'naive', got %q", mode)
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
	)
	if err != nil {
		log.Fatalf("kafka client: %v", err)
	}
	defer cli.Close()

	// Wait for cluster.
	for i := 0; i < 60; i++ {
		if err := cli.Ping(context.Background()); err == nil {
			break
		}
		time.Sleep(time.Second)
	}

	mx := newConsumerMetrics(mode)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("/metrics", metrics.Handler())
	mux.HandleFunc("/mode", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(mode)) })
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("consumer listening on :%s mode=%s topic=%s group=%s", port, mode, topic, group)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go runConsumer(ctx, cli, pool, consumerpkg.Mode(mode), mx)

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	cancel()
	shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShut()
	_ = srv.Shutdown(shutCtx)
}

func runConsumer(ctx context.Context, cli *kgo.Client, pool *pgxpool.Pool, mode consumerpkg.Mode, mx *consumerMetrics) {
	for {
		fetches := cli.PollFetches(ctx)
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				if errors.Is(e.Err, context.Canceled) {
					return
				}
				log.Printf("consumer: fetch error topic=%s partition=%d: %v", e.Topic, e.Partition, e.Err)
				mx.errors.Inc()
			}
		}
		fetches.EachRecord(func(rec *kgo.Record) {
			eventID, userID, amount := parseRecord(rec)
			if eventID == "" {
				eventID = string(rec.Key) + ":" + rec.Topic + ":" + recordOffset(rec)
			}
			if err := consumerpkg.Apply(ctx, pool, mode, eventID, userID, amount); err != nil {
				log.Printf("consumer: apply event_id=%s err=%v", eventID, err)
				mx.errors.Inc()
				return
			}
			mx.processed.Inc()
		})

		// Commit offsets after applying. Idempotent mode tolerates
		// double-replay; naive mode does not - that's the lesson.
		if err := cli.CommitUncommittedOffsets(ctx); err != nil {
			log.Printf("consumer: commit offsets: %v", err)
		}
	}
}

func parseRecord(rec *kgo.Record) (eventID, userID string, amount int64) {
	for _, h := range rec.Headers {
		if h.Key == "event_id" {
			eventID = string(h.Value)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Value, &body); err == nil {
		if v, ok := body["user_id"].(string); ok {
			userID = v
		}
		switch v := body["amount"].(type) {
		case float64:
			amount = int64(v)
		case int64:
			amount = v
		}
	}
	if userID == "" {
		userID = "user-replay"
	}
	if amount == 0 {
		amount = 1
	}
	return
}

func recordOffset(rec *kgo.Record) string {
	return formatInt64(rec.Offset)
}

// formatInt64 avoids pulling fmt for a single number.
func formatInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	const base = 10
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%base)
		n /= base
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

type consumerMetrics struct {
	processed prometheus.Counter
	errors    prometheus.Counter
}

func newConsumerMetrics(mode string) *consumerMetrics {
	common := prometheus.Labels{"mode": mode}
	m := &consumerMetrics{
		processed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "consumer_events_processed_total", Help: "Events handed to the consumer's apply.", ConstLabels: common,
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "consumer_errors_total", Help: "Consumer-side errors.", ConstLabels: common,
		}),
	}
	metrics.MustRegister(m.processed, m.errors)
	return m
}
