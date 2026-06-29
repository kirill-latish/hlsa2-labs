// Package metrics centralises the Prometheus vocabulary shared by
// every lab-5-3 binary (producer, consumer, outbox-relay). Every
// series is prefixed lab53_ so the recording rules and the "Event
// Pipeline" dashboard stay simple. The middleware mirrors lab 3-3's
// shared RED-shape so the HTTP control planes are observable too.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// latencyBuckets span sub-ms to ~30s so end-to-end processing latency
// stays on-scale even when a consumer is mid-crash recovery.
var latencyBuckets = []float64{
	0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1,
	0.25, 0.5, 1, 2.5, 5, 10, 30,
}

// HTTPMetrics is the shared RED-shape for every HTTP control plane in
// the lab. The service name is stamped into every observation.
type HTTPMetrics struct {
	Service        string
	RequestsTotal  *prometheus.CounterVec
	RequestSeconds *prometheus.HistogramVec
}

// NewHTTPMetrics wires the shared HTTP metrics for one service.
func NewHTTPMetrics(service string) *HTTPMetrics {
	labels := []string{"service", "endpoint", "method", "code"}
	return &HTTPMetrics{
		Service: service,
		RequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab53_http_requests_total",
			Help: "HTTP requests handled, broken out by service/endpoint/method/code.",
		}, labels),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab53_http_request_duration_seconds",
			Help:    "Server-side HTTP request duration.",
			Buckets: latencyBuckets,
		}, labels),
	}
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

// Middleware records RED metrics for every request. /metrics and
// /healthz typically belong in the skip set.
func (m *HTTPMetrics) Middleware(skip map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip != nil && skip[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(rec, r)
			elapsed := time.Since(start).Seconds()
			lbls := prometheus.Labels{
				"service":  m.Service,
				"endpoint": r.URL.Path,
				"method":   r.Method,
				"code":     strconv.Itoa(rec.code),
			}
			m.RequestsTotal.With(lbls).Inc()
			m.RequestSeconds.With(lbls).Observe(elapsed)
		})
	}
}

// Handler returns a ready-to-mount Prometheus /metrics handler bound to
// the default registry that promauto registers into.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Producer holds the producer-side pipeline metrics.
type Producer struct {
	EventsProduced     *prometheus.CounterVec // by key_strategy, type, mode
	DuplicatesEmitted  prometheus.Counter
	DualWriteCommitted *prometheus.CounterVec // by mode (db rows committed)
	PublishLatency     prometheus.Histogram
}

// NewProducer wires the producer-specific metrics. Called once from
// cmd/producer/main.go.
func NewProducer() *Producer {
	return &Producer{
		EventsProduced: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab53_events_produced_total",
			Help: "Events published by the producer, by partition-key strategy / type / publish mode.",
		}, []string{"key_strategy", "type", "mode"}),
		DuplicatesEmitted: promauto.NewCounter(prometheus.CounterOpts{
			Name: "lab53_duplicates_emitted_total",
			Help: "Deliberately re-published duplicate events (duplicate injection).",
		}),
		DualWriteCommitted: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab53_dualwrite_committed_total",
			Help: "Business rows committed to Postgres by publish mode (naive-dualwrite/outbox).",
		}, []string{"mode"}),
		PublishLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "lab53_publish_latency_seconds",
			Help:    "Producer publish (to Redpanda) latency.",
			Buckets: latencyBuckets,
		}),
	}
}

// Consumer holds the consumer-side pipeline metrics. The instance label
// is a const-label so consumer-1/2/3 each export their own series.
type Consumer struct {
	EventsConsumed      prometheus.Counter
	DuplicateSuppressed prometheus.Counter
	OrderingViolations  prometheus.Counter
	SideEffects         *prometheus.CounterVec // by kind
	ConsumerLagCount    prometheus.Gauge
	ConsumerLagAge      prometheus.Gauge
	ProcessingLatency   prometheus.Histogram
	Mode                *prometheus.GaugeVec // which mode/replay is active (1=on)
}

// NewConsumer wires the consumer-specific metrics for one instance.
func NewConsumer(instance string) *Consumer {
	cl := prometheus.Labels{"instance": instance}
	return &Consumer{
		EventsConsumed: promauto.NewCounter(prometheus.CounterOpts{
			Name: "lab53_events_consumed_total", Help: "Events processed by the consumer.", ConstLabels: cl,
		}),
		DuplicateSuppressed: promauto.NewCounter(prometheus.CounterOpts{
			Name: "lab53_duplicate_suppressed_total", Help: "Duplicate events suppressed by the dedup check.", ConstLabels: cl,
		}),
		OrderingViolations: promauto.NewCounter(prometheus.CounterOpts{
			Name: "lab53_ordering_violations_total", Help: "Out-of-order events detected via per-entity sequence numbers.", ConstLabels: cl,
		}),
		SideEffects: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab53_side_effects_total", Help: "External side effects fired by the consumer.", ConstLabels: cl,
		}, []string{"kind"}),
		ConsumerLagCount: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab53_consumer_lag_count", Help: "Unprocessed backlog (end offset - processed offset) summed across owned partitions.", ConstLabels: cl,
		}),
		ConsumerLagAge: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab53_consumer_lag_age_seconds", Help: "Age of the oldest unprocessed event (projection staleness).", ConstLabels: cl,
		}),
		ProcessingLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Name: "lab53_processing_latency_seconds", Help: "Per-event processing latency (consume to commit).", Buckets: latencyBuckets, ConstLabels: cl,
		}),
		Mode: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab53_consumer_mode_active", Help: "Active consumer mode / replay mode (1=on).", ConstLabels: cl,
		}, []string{"setting"}),
	}
}

// Relay holds the outbox-relay metrics.
type Relay struct {
	Published     prometheus.Counter
	PublishErrors prometheus.Counter
	OutboxBacklog prometheus.Gauge
	RelayLagAge   prometheus.Gauge
}

// NewRelay wires the outbox-relay metrics.
func NewRelay() *Relay {
	return &Relay{
		Published: promauto.NewCounter(prometheus.CounterOpts{
			Name: "lab53_outbox_published_total", Help: "Outbox rows successfully relayed to Redpanda.",
		}),
		PublishErrors: promauto.NewCounter(prometheus.CounterOpts{
			Name: "lab53_outbox_publish_errors_total", Help: "Outbox relay publish failures.",
		}),
		OutboxBacklog: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab53_outbox_backlog", Help: "Committed-but-unpublished rows in events_outbox.",
		}),
		RelayLagAge: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab53_outbox_lag_age_seconds", Help: "Age of the oldest unpublished outbox row.",
		}),
	}
}
