// Package metrics centralises the Prometheus metrics used by every
// lab-5-2 service. The producer, consumer fleet, and loadgen all share
// this vocabulary so the recording rules and the Pipeline Overview
// dashboard can stay simple. Every series is prefixed lab52_.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Standard latency histogram buckets - sub-ms to ~30s so retry/backoff
// processing times under fault injection stay on-scale.
var latencyBuckets = []float64{
	0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1,
	0.25, 0.5, 1, 2.5, 5, 10, 30,
}

// HTTPMetrics is the shared RED-shape for every HTTP control surface in
// the lab (producer control API, consumer admin/health, loadgen).
type HTTPMetrics struct {
	Service        string
	RequestsTotal  *prometheus.CounterVec
	RequestSeconds *prometheus.HistogramVec
}

func NewHTTPMetrics(service string) *HTTPMetrics {
	labels := []string{"service", "endpoint", "method", "code"}
	return &HTTPMetrics{
		Service: service,
		RequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab52_http_requests_total",
			Help: "HTTP requests handled, broken out by service/endpoint/method/code.",
		}, labels),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab52_http_request_duration_seconds",
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

// Handler returns a ready-to-mount Prometheus /metrics handler.
func Handler() http.Handler {
	return promhttp.Handler()
}

// MustRegister proxies to the default registry so callers that build
// ad-hoc collectors (with ConstLabels) don't import client_golang
// directly.
func MustRegister(cs ...prometheus.Collector) {
	prometheus.MustRegister(cs...)
}

// Producer holds the producer-side pipeline metrics. The producer is
// the single exporter of consumer-lag-count (it polls the RabbitMQ
// management API) so the dashboard reads one source.
type Producer struct {
	// MessagesProduced counts every successfully published message by
	// broker + message type (normal/poison/transient/permanent).
	MessagesProduced *prometheus.CounterVec
	// Errors counts publish failures by reason (nack / rejected /
	// connection). A non-zero rate means broker backpressure is
	// propagating to the producer.
	Errors *prometheus.CounterVec
	// LagCount is the number of messages waiting in the work queue
	// (consumer lag - count). Sampled from the RabbitMQ mgmt API.
	LagCount prometheus.Gauge
	// RateRPS is the configured target produce rate for the current run.
	RateRPS *prometheus.GaugeVec
	// BackpressureEnabled is 1 when the producer honors broker
	// backpressure (slows on nack), 0 when it ignores it.
	BackpressureEnabled prometheus.Gauge
}

func NewProducer() *Producer {
	return &Producer{
		MessagesProduced: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab52_messages_produced_total",
			Help: "Messages successfully published, by broker and message type.",
		}, []string{"broker", "type"}),
		Errors: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab52_producer_errors_total",
			Help: "Producer publish errors by reason (rejected/nack/conn).",
		}, []string{"reason"}),
		LagCount: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab52_consumer_lag_count",
			Help: "Messages waiting in the work queue (consumer lag - count).",
		}),
		RateRPS: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab52_producer_rate_rps",
			Help: "Configured target produce rate (msgs/s) for the current run.",
		}, []string{"label"}),
		BackpressureEnabled: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab52_producer_backpressure_enabled",
			Help: "1 when the producer honors broker backpressure, else 0.",
		}),
	}
}

// Consumer holds the per-consumer pipeline metrics. Each consumer
// process stamps its instance id via ConstLabels so the dashboard can
// break throughput, retries, and DLQ out per instance.
type Consumer struct {
	Acked             prometheus.Counter
	Retries           prometheus.Counter
	DLQ               prometheus.Counter
	ProcessingSeconds prometheus.Histogram
	OldestUnprocessed prometheus.Gauge
	Mode              *prometheus.GaugeVec
}

// NewConsumer builds the consumer metrics with consumer=<id> as a
// constant label and registers them on the default registry.
func NewConsumer(consumerID string) *Consumer {
	cl := prometheus.Labels{"consumer": consumerID}
	c := &Consumer{
		Acked: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "lab52_messages_acked_total", Help: "Messages processed and acked (per-consumer throughput).", ConstLabels: cl,
		}),
		Retries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "lab52_retries_total", Help: "Retry attempts scheduled by this consumer.", ConstLabels: cl,
		}),
		DLQ: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "lab52_dlq_total", Help: "Messages routed to the dead-letter queue by this consumer.", ConstLabels: cl,
		}),
		ProcessingSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "lab52_processing_duration_seconds", Help: "Per-message processing time, receive to ack.", Buckets: latencyBuckets, ConstLabels: cl,
		}),
		OldestUnprocessed: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "lab52_oldest_unprocessed_age_seconds", Help: "Age of the most recently dequeued message (consumer lag - time).", ConstLabels: cl,
		}),
		Mode: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab52_consumer_mode", Help: "Active consumer retry semantics (1 for the live mode).", ConstLabels: cl,
		}, []string{"mode"}),
	}
	MustRegister(c.Acked, c.Retries, c.DLQ, c.ProcessingSeconds, c.OldestUnprocessed, c.Mode)
	return c
}
