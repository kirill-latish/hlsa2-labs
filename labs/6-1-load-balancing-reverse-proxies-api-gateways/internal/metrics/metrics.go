// Package metrics centralises the Prometheus metrics used by every
// lab-6-1 service. The edge-proxy, backends, and loadgen all share
// this vocabulary so the recording rules and the Edge Overview
// dashboard can stay simple.
//
// The metric prefix is lab61_ across the board.
package metrics

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// latencyBuckets are tuned so the *edge overhead* (sub-millisecond to a
// few ms) stays on-scale alongside slow backend endpoints (hundreds of
// ms) and the 504 timeout (~2s). This is the headline-number lab, so
// the small buckets matter.
var latencyBuckets = []float64{
	0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05,
	0.1, 0.25, 0.5, 1, 2.5, 5,
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
			Name: "lab61_http_requests_total",
			Help: "HTTP requests handled, broken out by service/endpoint/method/code.",
		}, labels),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab61_http_request_duration_seconds",
			Help:    "Server-side HTTP request duration.",
			Buckets: latencyBuckets,
		}, labels),
		BytesSent: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab61_http_response_bytes_total",
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

// Hijack lets the RED middleware sit in front of handlers that need to
// hijack the connection (the backend's "connection refused" fault that
// makes the edge emit 502).
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := s.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
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

// Edge holds the edge-proxy-specific metrics the Edge Overview
// dashboard reads from. Exported only by cmd/edge-proxy.
type Edge struct {
	// OverheadSeconds is THE headline number: total request time at the
	// edge MINUS the backend's self-reported processing time. This is
	// the latency the edge tier itself adds (routing + proxy + network
	// + body copy), measured separately from backend processing.
	OverheadSeconds prometheus.Histogram
	// RequestSeconds is the total edge-observed request duration.
	RequestSeconds *prometheus.HistogramVec
	// BackendRequestsTotal is the per-backend request distribution
	// counter (label backend) used by the distribution experiment.
	BackendRequestsTotal *prometheus.CounterVec
	// BackendUp is each backend's health state as the proxy sees it
	// (1=up, 0=down), driven by the active health-check loop.
	BackendUp *prometheus.GaugeVec
	// BackendInflight is the current in-flight request count per
	// backend (the signal least-conn balances on).
	BackendInflight *prometheus.GaugeVec
	// FiveXXTotal counts edge-emitted 5xx by class (502/503/504) and
	// passed-through backend 5xx, labelled by code.
	FiveXXTotal *prometheus.CounterVec
	// HealthCheckFailTotal counts failed active health checks per
	// backend (label depth too, so deep vs shallow is visible).
	HealthCheckFailTotal *prometheus.CounterVec
	// AlgoActive/DepthActive are 1 for the currently selected option so
	// the dashboard can label which run is which.
	AlgoActive  *prometheus.GaugeVec
	DepthActive *prometheus.GaugeVec
	// ConfigValue exports numeric config knobs (interval_ms, threshold).
	ConfigValue *prometheus.GaugeVec
}

func NewEdge() *Edge {
	return &Edge{
		OverheadSeconds: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "lab61_edge_overhead_seconds",
			Help:    "Edge latency overhead = total edge time minus backend self-reported processing time.",
			Buckets: latencyBuckets,
		}),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab61_edge_request_duration_seconds",
			Help:    "Total edge-observed request duration (overhead + backend processing).",
			Buckets: latencyBuckets,
		}, []string{"code"}),
		BackendRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab61_edge_backend_requests_total",
			Help: "Requests routed to each backend (the load-distribution counter).",
		}, []string{"backend"}),
		BackendUp: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab61_edge_backend_up",
			Help: "Backend health state as the proxy sees it (1=up, 0=down).",
		}, []string{"backend"}),
		BackendInflight: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab61_edge_backend_inflight",
			Help: "Current in-flight requests per backend (the least-conn signal).",
		}, []string{"backend"}),
		FiveXXTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab61_edge_5xx_total",
			Help: "Edge 5xx responses by class: 502=connectivity, 503=no healthy backends, 504=backend timeout (plus passed-through backend 5xx).",
		}, []string{"code"}),
		HealthCheckFailTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab61_edge_healthcheck_fail_total",
			Help: "Failed active health checks, by backend and depth (shallow/deep).",
		}, []string{"backend", "depth"}),
		AlgoActive: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab61_edge_algo_active",
			Help: "Currently active balancing algorithm (1=active).",
		}, []string{"algo"}),
		DepthActive: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab61_edge_healthcheck_depth_active",
			Help: "Currently active health-check depth (1=active).",
		}, []string{"depth"}),
		ConfigValue: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab61_edge_config_value",
			Help: "Numeric edge config knobs (param=health_interval_ms|failure_threshold).",
		}, []string{"param"}),
	}
}

// Backend holds the backend-instance-specific metrics. Exported by
// cmd/backend (one process per backend-N instance).
type Backend struct {
	// ProcessSeconds is the backend's self-measured processing time per
	// endpoint - the exact number it stamps into X-Backend-Process-Ms.
	ProcessSeconds *prometheus.HistogramVec
	// HealthChecksTotal counts inbound health checks by depth + result.
	HealthChecksTotal *prometheus.CounterVec
	// Broken is 1 when this instance has a fault injected.
	Broken prometheus.Gauge
}

func NewBackend(name string) *Backend {
	return &Backend{
		ProcessSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "lab61_backend_process_seconds",
			Help:        "Backend self-measured processing time per endpoint.",
			Buckets:     latencyBuckets,
			ConstLabels: prometheus.Labels{"backend": name},
		}, []string{"endpoint"}),
		HealthChecksTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name:        "lab61_backend_healthcheck_total",
			Help:        "Inbound health checks by depth (shallow/deep) and result (ok/fail).",
			ConstLabels: prometheus.Labels{"backend": name},
		}, []string{"depth", "result"}),
		Broken: promauto.NewGauge(prometheus.GaugeOpts{
			Name:        "lab61_backend_broken",
			Help:        "Whether a fault is currently injected on this backend (1=yes).",
			ConstLabels: prometheus.Labels{"backend": name},
		}),
	}
}

// Loadgen exports the offered vs served rate and per-label totals. It
// is imported by cmd/loadgen so the dashboard reads from one source.
type Loadgen struct {
	OfferedTotal   *prometheus.CounterVec
	ServedTotal    *prometheus.CounterVec
	RequestSeconds *prometheus.HistogramVec
	OutcomeTotal   *prometheus.CounterVec
	RateRPS        *prometheus.GaugeVec
}

func NewLoadgen() *Loadgen {
	lbls := []string{"label"}
	return &Loadgen{
		OfferedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab61_loadgen_offered_total",
			Help: "Total requests the loadgen intended to send (the offered rate).",
		}, lbls),
		ServedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab61_loadgen_served_total",
			Help: "Requests that completed successfully (HTTP 2xx).",
		}, lbls),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab61_loadgen_request_duration_seconds",
			Help:    "Loadgen-observed end-to-end latency.",
			Buckets: latencyBuckets,
		}, []string{"label", "cost", "code"}),
		OutcomeTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab61_loadgen_outcome_total",
			Help: "Loadgen request outcomes by cost (fast/slow) and HTTP code.",
		}, []string{"label", "cost", "code"}),
		RateRPS: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab61_loadgen_rate_rps",
			Help: "Configured target arrival rate (RPS) for the current run.",
		}, lbls),
	}
}
