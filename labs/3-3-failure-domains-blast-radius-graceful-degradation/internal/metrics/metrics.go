// Package metrics centralises the Prometheus metrics used by every
// lab-3-3 service. The gateway, deps, loadgen, and fault-injector all
// share this vocabulary so the recording rules and the Resilience
// Overview dashboard can stay simple.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Standard latency histogram buckets - sub-ms to ~30s so the retry
// storm in step 7 stays on-scale.
var latencyBuckets = []float64{
	0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1,
	0.25, 0.5, 1, 2.5, 5, 10, 30,
}

// HTTPMetrics is the shared RED-shape for every HTTP server in the
// lab. Service name is set in NewHTTPMetrics and stamped into every
// observation.
type HTTPMetrics struct {
	Service        string
	RequestsTotal  *prometheus.CounterVec
	RequestSeconds *prometheus.HistogramVec
	BytesSent      *prometheus.CounterVec
}

func NewHTTPMetrics(service string) *HTTPMetrics {
	labels := []string{"service", "endpoint", "method", "code"}
	return &HTTPMetrics{
		Service: service,
		RequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_http_requests_total",
			Help: "HTTP requests handled, broken out by service/endpoint/method/code.",
		}, labels),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab33_http_request_duration_seconds",
			Help:    "Server-side HTTP request duration.",
			Buckets: latencyBuckets,
		}, labels),
		BytesSent: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_http_response_bytes_total",
			Help: "Total bytes written to HTTP responses.",
		}, []string{"service", "endpoint"}),
	}
}

type statusRecorder struct {
	http.ResponseWriter
	code  int
	bytes int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
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
			m.BytesSent.With(prometheus.Labels{
				"service":  m.Service,
				"endpoint": r.URL.Path,
			}).Add(float64(rec.bytes))
		})
	}
}

// Handler returns a ready-to-mount Prometheus /metrics handler.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Resilience holds the lab-specific metrics that the Resilience
// Overview dashboard reads from. They are exported by the gateway.
type Resilience struct {
	// DepCallsTotal counts each gateway -> dep HTTP attempt, labelled
	// by dep + outcome (success/error/breaker_open/timeout/shed).
	DepCallsTotal *prometheus.CounterVec
	// DepCallSeconds is the duration of each dep call.
	DepCallSeconds *prometheus.HistogramVec
	// CriticalJourneyTotal counts inbound /checkout outcomes from the
	// gateway's perspective (success_full / success_degraded / failed).
	CriticalJourneyTotal *prometheus.CounterVec
	// PoolInflight is a gauge of in-flight requests per dep.
	PoolInflight *prometheus.GaugeVec
	// PoolMax is the configured max in-flight per dep (constant gauge,
	// makes "% used" panels trivial).
	PoolMax *prometheus.GaugeVec
	// BreakerState exports each per-dep breaker as 0=closed, 1=halfopen, 2=open.
	BreakerState *prometheus.GaugeVec
	// FallbacksServedTotal counts how often the gateway returned LKG
	// or omit-the-widget content instead of the live dep response.
	FallbacksServedTotal *prometheus.CounterVec
	// RetriesTotal counts gateway retry attempts (label tells you if
	// the global retry budget was consumed or denied).
	RetriesTotal *prometheus.CounterVec
	// SheddedTotal counts inbound 429 or per-dep 503 shed events.
	SheddedTotal *prometheus.CounterVec
	// ControlStatus is a constant-style gauge exporting which knobs
	// are currently on (so the dashboard can label which run is which).
	ControlStatus *prometheus.GaugeVec
}

// NewResilience wires every gateway-specific resilience metric. Called
// exactly once from cmd/gateway/main.go.
func NewResilience() *Resilience {
	depLabels := []string{"dep", "critical"}
	return &Resilience{
		DepCallsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_gateway_dep_calls_total",
			Help: "Gateway -> dep HTTP attempts by outcome (success/error/breaker_open/timeout/shed/fallback).",
		}, []string{"dep", "critical", "outcome"}),
		DepCallSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab33_gateway_dep_call_duration_seconds",
			Help:    "Gateway-observed dep call duration.",
			Buckets: latencyBuckets,
		}, []string{"dep", "critical"}),
		CriticalJourneyTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_gateway_checkout_total",
			Help: "Inbound /checkout outcomes (success_full/success_degraded/failed/shed).",
		}, []string{"outcome"}),
		PoolInflight: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab33_gateway_pool_inflight",
			Help: "Current in-flight requests per dep pool.",
		}, depLabels),
		PoolMax: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab33_gateway_pool_max",
			Help: "Configured pool capacity per dep (constant).",
		}, depLabels),
		BreakerState: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab33_gateway_breaker_state",
			Help: "Per-dep breaker state: 0=closed, 1=halfopen, 2=open.",
		}, depLabels),
		FallbacksServedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_gateway_fallbacks_served_total",
			Help: "Number of dep responses served from LKG cache or omitted.",
		}, []string{"dep", "kind"}),
		RetriesTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_gateway_retries_total",
			Help: "Per-dep retry attempts (outcome=consumed_budget|denied_budget|disabled).",
		}, []string{"dep", "outcome"}),
		SheddedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_gateway_shed_total",
			Help: "Shed events (scope=inbound_429|dep_503).",
		}, []string{"scope", "dep"}),
		ControlStatus: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab33_gateway_control_enabled",
			Help: "Whether each resilience control is on for the current run (1=on, 0=off).",
		}, []string{"control"}),
	}
}

// Loadgen exports the offered vs served rate, retry counts, and per-
// label totals. It is imported by cmd/loadgen so the dashboard reads
// from one source instead of stitching gateway + loadgen metrics.
type Loadgen struct {
	OfferedTotal       *prometheus.CounterVec
	ServedTotal        *prometheus.CounterVec
	RetriesTotal       *prometheus.CounterVec
	RequestSeconds     *prometheus.HistogramVec
	OutcomeTotal       *prometheus.CounterVec
	RateRPS            *prometheus.GaugeVec
}

func NewLoadgen() *Loadgen {
	lbls := []string{"label", "profile"}
	return &Loadgen{
		OfferedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_loadgen_offered_total",
			Help: "Total requests the loadgen *intended* to send (the offered rate, including retries).",
		}, lbls),
		ServedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_loadgen_served_total",
			Help: "Requests that completed successfully (HTTP 2xx).",
		}, lbls),
		RetriesTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_loadgen_retries_total",
			Help: "Retries the loadgen issued on top of base offered rate.",
		}, lbls),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab33_loadgen_request_duration_seconds",
			Help:    "Loadgen-observed end-to-end latency.",
			Buckets: latencyBuckets,
		}, []string{"label", "profile", "code"}),
		OutcomeTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab33_loadgen_outcome_total",
			Help: "Loadgen request outcomes by HTTP code.",
		}, []string{"label", "profile", "code"}),
		RateRPS: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab33_loadgen_rate_rps",
			Help: "Configured target arrival rate (RPS) for the current run.",
		}, lbls),
	}
}

// FaultInjector exports the current fault state so dashboards can
// annotate runs and so the system has *one* source of truth for what
// fault is live.
type FaultInjector struct {
	FaultActive *prometheus.GaugeVec
}

func NewFaultInjector() *FaultInjector {
	return &FaultInjector{
		FaultActive: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab33_fault_active",
			Help: "Whether a fault is currently injected for this dep (1=yes, 0=no), labelled by mode.",
		}, []string{"dep", "mode"}),
	}
}
