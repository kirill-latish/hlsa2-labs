// producer publishes messages to the active broker at a controlled
// rate and is the single fault-injection surface for the pipeline.
//
// Control API:
//
//	POST   /start  {"rate":300,"duration_s":300,"label":"baseline"}
//	POST   /stop
//	GET    /state
//	GET    /summary
//	POST   /admin/config  {"poison_count":1,"transient_rate":0.1,
//	                        "permanent_rate":0.02,"overload_multiplier":1,
//	                        "backpressure":true}
//	GET    /admin/config
//	GET    /healthz / /metrics
//
// Fault injection is expressed by stamping each message with a type the
// consumer knows how to (mis)handle: poison, transient, permanent, or
// normal. The producer also uses publisher confirms so a broker that
// rejects publishes (bounded queue + reject-publish overflow) surfaces
// as backpressure the producer can either honor or ignore.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	mrand "math/rand"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/hlsa2-labs/lab5-2/internal/metrics"
	"github.com/hlsa2-labs/lab5-2/internal/pipeline"
)

var (
	errRejected      = errors.New("publish rejected by broker (backpressure)")
	errConfirmTimout = errors.New("publish confirm timed out")
)

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

// injConfig holds the live fault-injection knobs. Mutated via
// /admin/config between runs.
type injConfig struct {
	PoisonCount        int     `json:"poison_count"`
	TransientRate      float64 `json:"transient_rate"`
	PermanentRate      float64 `json:"permanent_rate"`
	OverloadMultiplier float64 `json:"overload_multiplier"`
	Backpressure       bool    `json:"backpressure"`
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

	produced int64
	errs     int64
}

func (s *runState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"running":        s.running,
		"label":          s.label,
		"started_at":     s.startedAt.UTC().Format(time.RFC3339Nano),
		"ends_at":        s.endsAt.UTC().Format(time.RFC3339Nano),
		"rate_rps":       s.rateRPS,
		"duration_s":     s.durationS,
		"produced":       atomic.LoadInt64(&s.produced),
		"produce_errors": atomic.LoadInt64(&s.errs),
	}
}

// Producer owns the broker connections and the publish path.
type Producer struct {
	broker string

	// RabbitMQ publish channel guarded by pubMu so confirms stay
	// matched to publishes.
	pubMu    sync.Mutex
	ch       *amqp.Channel
	confirms chan amqp.Confirmation

	// Redpanda client; only non-nil when broker=redpanda.
	kcli *kgo.Client

	mx *metrics.Producer
}

func main() {
	port := envOrDefault("PORT", "8080")
	broker := envOrDefault("BROKER", "rabbitmq")
	amqpURL := envOrDefault("RABBITMQ_URL", "amqp://guest:guest@rabbitmq:5672/")
	mgmtURL := envOrDefault("RABBITMQ_MGMT_URL", "http://rabbitmq:15672")
	mgmtUser := envOrDefault("RABBITMQ_MGMT_USER", "guest")
	mgmtPass := envOrDefault("RABBITMQ_MGMT_PASS", "guest")
	redpandaBrokers := envOrDefault("REDPANDA_BROKERS", "redpanda:9092")
	maxLen := envInt("QUEUE_MAX_LEN", 50000)

	mx := metrics.NewProducer()

	conn, err := pipeline.Connect(amqpURL, 60)
	if err != nil {
		log.Fatalf("rabbitmq connect: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		log.Fatalf("rabbitmq channel: %v", err)
	}
	if err := pipeline.DeclareTopology(ch, maxLen); err != nil {
		log.Fatalf("declare topology: %v", err)
	}
	if err := ch.Confirm(false); err != nil {
		log.Fatalf("enable publisher confirms: %v", err)
	}

	p := &Producer{
		broker:   broker,
		ch:       ch,
		confirms: ch.NotifyPublish(make(chan amqp.Confirmation, 1)),
		mx:       mx,
	}
	if broker == "redpanda" {
		kcli, err := kgo.NewClient(
			kgo.SeedBrokers(redpandaBrokers),
			kgo.DefaultProduceTopic(pipeline.RedpandaTopic),
		)
		if err != nil {
			log.Fatalf("redpanda client: %v", err)
		}
		defer kcli.Close()
		p.kcli = kcli
	}

	state := &runState{}
	cfg := &injConfig{OverloadMultiplier: 1.0}
	var cfgMu sync.RWMutex
	mx.BackpressureEnabled.Set(0)

	// Lag reporter: sample queue depth from the RabbitMQ mgmt API.
	go reportLag(mgmtURL, mgmtUser, mgmtPass, mx)

	httpMetrics := metrics.NewHTTPMetrics("producer")
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", metrics.Handler())

	r.Get("/state", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, state.snapshot())
	})
	r.Get("/summary", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, state.snapshot())
	})

	r.Get("/admin/config", func(w http.ResponseWriter, _ *http.Request) {
		cfgMu.RLock()
		defer cfgMu.RUnlock()
		writeJSON(w, cfg)
	})
	r.Post("/admin/config", func(w http.ResponseWriter, r *http.Request) {
		var body injConfig
		body.OverloadMultiplier = -1
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfgMu.Lock()
		cfg.PoisonCount = body.PoisonCount
		cfg.TransientRate = body.TransientRate
		cfg.PermanentRate = body.PermanentRate
		if body.OverloadMultiplier >= 0 {
			cfg.OverloadMultiplier = body.OverloadMultiplier
		}
		if cfg.OverloadMultiplier == 0 {
			cfg.OverloadMultiplier = 1.0
		}
		cfg.Backpressure = body.Backpressure
		snap := *cfg
		cfgMu.Unlock()
		if snap.Backpressure {
			mx.BackpressureEnabled.Set(1)
		} else {
			mx.BackpressureEnabled.Set(0)
		}
		log.Printf("admin/config: %+v", snap)
		writeJSON(w, snap)
	})

	r.Post("/stop", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		if state.cancel != nil {
			state.cancel()
		}
		state.running = false
		state.mu.Unlock()
		_, _ = w.Write([]byte("stopped"))
	})

	r.Post("/start", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Rate      int    `json:"rate"`
			DurationS int    `json:"duration_s"`
			Label     string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Rate <= 0 {
			req.Rate = 240
		}
		if req.DurationS <= 0 {
			req.DurationS = 60
		}
		if req.Label == "" {
			req.Label = "run"
		}

		cfgMu.RLock()
		snapCfg := *cfg
		cfgMu.RUnlock()

		state.mu.Lock()
		if state.running && state.cancel != nil {
			state.cancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		state.running = true
		state.cancel = cancel
		state.label = req.Label
		state.startedAt = time.Now()
		state.endsAt = time.Now().Add(time.Duration(req.DurationS) * time.Second)
		state.rateRPS = req.Rate
		state.durationS = req.DurationS
		atomic.StoreInt64(&state.produced, 0)
		atomic.StoreInt64(&state.errs, 0)
		state.mu.Unlock()

		mx.RateRPS.WithLabelValues(req.Label).Set(float64(req.Rate))
		go p.drive(ctx, req.Rate, req.DurationS, req.Label, snapCfg, state)
		writeJSON(w, state.snapshot())
	})

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("producer listening on :%s broker=%s", port, broker)
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

// drive publishes at the configured rate until duration elapses or the
// run is cancelled. poisonRemaining is seeded from the live injection
// config so the canonical poison messages go out near the start of the
// run.
func (p *Producer) drive(ctx context.Context, rate, durationS int, label string, cfg injConfig, state *runState) {
	mult := cfg.OverloadMultiplier
	if mult <= 0 {
		mult = 1.0
	}
	effRate := float64(rate) * mult
	if effRate < 1 {
		effRate = 1
	}
	tick := time.Duration(float64(time.Second) / effRate)
	if tick <= 0 {
		tick = time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	deadline := time.Now().Add(time.Duration(durationS) * time.Second)

	rng := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	var poisonRemaining int64 = int64(cfg.PoisonCount)
	var seq int64

	finish := func() {
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
			typ := pickType(rng, cfg, &poisonRemaining)
			seq++
			m := pipeline.Message{
				ID:         newID(),
				Seq:        seq,
				Type:       typ,
				Label:      label,
				EnqueuedAt: time.Now(),
				Payload:    fmt.Sprintf("%s-%d", label, seq),
			}
			if typ == pipeline.TypePoison {
				// Unprocessable payload: not valid JSON for the
				// consumer's strict body parse.
				m.Payload = "\x00POISON\x00"
			}
			if err := p.publish(ctx, m); err != nil {
				atomic.AddInt64(&state.errs, 1)
				reason := "conn"
				if errors.Is(err, errRejected) {
					reason = "rejected"
				} else if errors.Is(err, errConfirmTimout) {
					reason = "nack"
				}
				p.mx.Errors.WithLabelValues(reason).Inc()
				// Honor backpressure: slow down when the broker is
				// rejecting. When disabled, keep hammering and let lag
				// grow unbounded - that's the failure mode step 6
				// reveals.
				if cfg.Backpressure {
					time.Sleep(50 * time.Millisecond)
				}
				continue
			}
			atomic.AddInt64(&state.produced, 1)
			p.mx.MessagesProduced.WithLabelValues(p.broker, string(typ)).Inc()
		}
	}
}

func pickType(rng *mrand.Rand, cfg injConfig, poisonRemaining *int64) pipeline.MessageType {
	if atomic.AddInt64(poisonRemaining, -1) >= 0 {
		return pipeline.TypePoison
	}
	atomic.StoreInt64(poisonRemaining, 0)
	r := rng.Float64()
	if r < cfg.PermanentRate {
		return pipeline.TypePermanent
	}
	if r < cfg.PermanentRate+cfg.TransientRate {
		return pipeline.TypeTransient
	}
	return pipeline.TypeNormal
}

// publish routes to the active broker. RabbitMQ is the primary path for
// retry/DLQ semantics; Redpanda is available for broker-family
// comparison.
func (p *Producer) publish(ctx context.Context, m pipeline.Message) error {
	if p.broker == "redpanda" && p.kcli != nil {
		return p.publishRedpanda(ctx, m)
	}
	return p.publishRabbit(ctx, m)
}

func (p *Producer) publishRabbit(ctx context.Context, m pipeline.Message) error {
	body, _ := json.Marshal(m)
	p.pubMu.Lock()
	defer p.pubMu.Unlock()
	err := p.ch.PublishWithContext(ctx, pipeline.WorkExchange, pipeline.WorkRoutingKey, false, false, amqp.Publishing{
		ContentType: "application/json",
		MessageId:   m.ID,
		Timestamp:   m.EnqueuedAt,
		Body:        body,
		Headers: amqp.Table{
			pipeline.HeaderRetryCount: int32(0),
			pipeline.HeaderType:       string(m.Type),
		},
	})
	if err != nil {
		return err
	}
	select {
	case c := <-p.confirms:
		if !c.Ack {
			return errRejected
		}
		return nil
	case <-time.After(3 * time.Second):
		return errConfirmTimout
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Producer) publishRedpanda(ctx context.Context, m pipeline.Message) error {
	body, _ := json.Marshal(m)
	rec := &kgo.Record{
		Topic: pipeline.RedpandaTopic,
		Key:   []byte(m.ID),
		Value: body,
		Headers: []kgo.RecordHeader{
			{Key: pipeline.HeaderType, Value: []byte(m.Type)},
		},
	}
	return p.kcli.ProduceSync(ctx, rec).FirstErr()
}

// reportLag samples the work queue depth from the RabbitMQ management
// API every 2s and exports it as the consumer-lag-count gauge.
func reportLag(mgmtURL, user, pass string, mx *metrics.Producer) {
	client := &http.Client{Timeout: 3 * time.Second}
	url := mgmtURL + "/api/queues/%2F/" + pipeline.WorkQueue
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		req.SetBasicAuth(user, pass)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var q struct {
			MessagesReady int64 `json:"messages_ready"`
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := json.Unmarshal(b, &q); err == nil {
			mx.LagCount.Set(float64(q.MessagesReady))
		}
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b[:])
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
