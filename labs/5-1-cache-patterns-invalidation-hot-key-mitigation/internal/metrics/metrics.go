// Package metrics centralises the Prometheus metrics used by every
// lab-5-1 service. The app and loadgen share this vocabulary so the
// recording rules and the Cache Overview dashboard can stay simple.
//
// Every metric name carries the lab51_ prefix so a single Prometheus
// can host several labs without collisions.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Standard latency histogram buckets - sub-ms to ~30s so a stampede
// (hot-key p99 spiking into seconds) stays on-scale.
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
			Name: "lab51_http_requests_total",
			Help: "HTTP requests handled, broken out by service/endpoint/method/code.",
		}, labels),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab51_http_request_duration_seconds",
			Help:    "Server-side HTTP request duration.",
			Buckets: latencyBuckets,
		}, labels),
		BytesSent: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab51_http_response_bytes_total",
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

// Cache holds the lab-specific metrics that the Cache Overview
// dashboard reads from. They are exported by the app.
type Cache struct {
	// RequestsTotal counts every cache read by outcome:
	//   hit        - served from the shared (sharded) Redis
	//   local_hit  - served from the in-process LRU
	//   miss       - not in any cache; a source fetch was needed
	//   stale_hit  - SWR served a stale value while refreshing async
	RequestsTotal *prometheus.CounterVec
	// MissesTotal counts shared-cache misses (denominator of fan-in).
	MissesTotal prometheus.Counter
	// SourceFetchesTotal counts actual Postgres SoR fetches. With no
	// coalescing each concurrent miss becomes its own fetch (stampede);
	// with singleflight/xfetch/swr the fan-in collapses toward one
	// fetch per logical expiry.
	SourceFetchesTotal prometheus.Counter
	// RedisOpsTotal counts per-node Redis operations so per-shard
	// imbalance (the hot-key story) is visible. node=redis-1|2|3.
	RedisOpsTotal *prometheus.CounterVec
	// EvictionsTotal counts local-LRU evictions (scope=local).
	EvictionsTotal *prometheus.CounterVec
	// ReadSeconds is the app-observed read latency by outcome.
	ReadSeconds *prometheus.HistogramVec
	// SourceFetchSeconds is the latency of the SoR fetch path.
	SourceFetchSeconds prometheus.Histogram
	// LocalSizeGauge exports the current local-LRU occupancy.
	LocalSizeGauge prometheus.Gauge
	// ConfigTTLSeconds / ConfigJitterPct / ConfigLocalLRU export the
	// active runtime knobs so a labelled run can be annotated.
	ConfigTTLSeconds prometheus.Gauge
	ConfigJitterPct  prometheus.Gauge
	ConfigLocalLRU   prometheus.Gauge
	// ConfigInfo is an info-style gauge (always 1) whose label set
	// records the active coalescing + invalidation strategy.
	ConfigInfo *prometheus.GaugeVec
}

// NewCache wires every app-specific cache metric. Called exactly once
// from cmd/app/main.go.
func NewCache() *Cache {
	return &Cache{
		RequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab51_cache_requests_total",
			Help: "Cache reads by outcome (hit/local_hit/miss/stale_hit).",
		}, []string{"outcome"}),
		MissesTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "lab51_cache_misses_total",
			Help: "Shared-cache misses (fan-in denominator).",
		}),
		SourceFetchesTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "lab51_cache_source_fetches_total",
			Help: "Actual Postgres SoR fetches (fan-in numerator).",
		}),
		RedisOpsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab51_redis_ops_total",
			Help: "Per-node Redis operations by op (get/set/del).",
		}, []string{"node", "op"}),
		EvictionsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab51_cache_evictions_total",
			Help: "Cache evictions by scope (local).",
		}, []string{"scope"}),
		ReadSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab51_cache_read_duration_seconds",
			Help:    "App-observed cache read latency by outcome.",
			Buckets: latencyBuckets,
		}, []string{"outcome"}),
		SourceFetchSeconds: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "lab51_cache_source_fetch_duration_seconds",
			Help:    "Latency of the SoR (Postgres) fetch path.",
			Buckets: latencyBuckets,
		}),
		LocalSizeGauge: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab51_local_lru_entries",
			Help: "Current number of entries in the in-process LRU.",
		}),
		ConfigTTLSeconds: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab51_config_ttl_seconds",
			Help: "Active base cache TTL in seconds.",
		}),
		ConfigJitterPct: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab51_config_ttl_jitter_pct",
			Help: "Active TTL jitter percentage.",
		}),
		ConfigLocalLRU: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab51_config_local_lru_enabled",
			Help: "Whether the local LRU is enabled (1=on, 0=off).",
		}),
		ConfigInfo: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab51_config_info",
			Help: "Active config info gauge (always 1); labels carry the strategy names.",
		}, []string{"coalescing", "invalidation", "local_lru"}),
	}
}

// Loadgen exports the offered vs served rate plus the staleness
// measurements collected by the writer/reader race. The dashboard
// reads from one source instead of stitching app + loadgen metrics.
type Loadgen struct {
	OfferedTotal   *prometheus.CounterVec
	ServedTotal    *prometheus.CounterVec
	RequestSeconds *prometheus.HistogramVec
	OutcomeTotal   *prometheus.CounterVec
	RateRPS        *prometheus.GaugeVec
	// StalenessSamplesTotal counts reader samples by result
	// (fresh|stale) during a staleness race.
	StalenessSamplesTotal *prometheus.CounterVec
	// StalenessMaxSeconds is the maximum observed staleness duration.
	StalenessMaxSeconds *prometheus.GaugeVec
}

func NewLoadgen() *Loadgen {
	lbls := []string{"label", "mode"}
	return &Loadgen{
		OfferedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab51_loadgen_offered_total",
			Help: "Total requests the loadgen intended to send (offered rate).",
		}, lbls),
		ServedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab51_loadgen_served_total",
			Help: "Requests that completed successfully (HTTP 2xx).",
		}, lbls),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab51_loadgen_request_duration_seconds",
			Help:    "Loadgen-observed end-to-end latency.",
			Buckets: latencyBuckets,
		}, []string{"label", "mode", "code"}),
		OutcomeTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab51_loadgen_outcome_total",
			Help: "Loadgen request outcomes by HTTP code.",
		}, []string{"label", "mode", "code"}),
		RateRPS: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab51_loadgen_rate_rps",
			Help: "Configured target arrival rate (RPS) for the current run.",
		}, lbls),
		StalenessSamplesTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab51_staleness_samples_total",
			Help: "Reader samples by result (fresh|stale) during a staleness race.",
		}, []string{"label", "strategy", "result"}),
		StalenessMaxSeconds: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab51_staleness_max_seconds",
			Help: "Maximum observed staleness duration (seconds).",
		}, []string{"label", "strategy"}),
	}
}
