// Package metrics centralises the Prometheus metrics used by every
// lab-6-2 service. The cache-proxy (role pop|shield), origin, and
// loadgen all share this vocabulary so the recording rules and the
// "Edge Delivery" dashboard can stay simple.
//
// Every metric name is prefixed lab62_ so a single grep tells you what
// belongs to this lab.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Standard latency histogram buckets - sub-ms to ~30s so a thundering
// herd that stalls the origin stays on-scale.
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
			Name: "lab62_http_requests_total",
			Help: "HTTP requests handled, broken out by service/endpoint/method/code.",
		}, labels),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab62_http_request_duration_seconds",
			Help:    "Server-side HTTP request duration.",
			Buckets: latencyBuckets,
		}, labels),
		BytesSent: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab62_http_response_bytes_total",
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
// /healthz typically belong in the skip set. We normalise the endpoint
// to the route's first path segment so per-object IDs don't explode the
// label cardinality on the proxy/origin.
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
				"endpoint": routeClass(r.URL.Path),
				"method":   r.Method,
				"code":     strconv.Itoa(rec.code),
			}
			m.RequestsTotal.With(lbls).Inc()
			m.RequestSeconds.With(lbls).Observe(elapsed)
			m.BytesSent.With(prometheus.Labels{
				"service":  m.Service,
				"endpoint": routeClass(r.URL.Path),
			}).Add(float64(rec.bytes))
		})
	}
}

// routeClass collapses /obj/s3 -> /obj so histogram cardinality stays
// bounded regardless of how many objects the catalog holds.
func routeClass(path string) string {
	if len(path) == 0 || path == "/" {
		return "/"
	}
	// keep the leading slash + first segment.
	seg := path[1:]
	for i := 0; i < len(seg); i++ {
		if seg[i] == '/' {
			return "/" + seg[:i]
		}
	}
	return "/" + seg
}

// Handler returns a ready-to-mount Prometheus /metrics handler.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Edge holds the cache-proxy metrics that the Edge Delivery dashboard
// reads from. Both the PoP and the shield export these (the node label
// distinguishes them).
type Edge struct {
	// CacheResponses counts every proxied response by cache status
	// (HIT/MISS/EXPIRED/STALE/BYPASS), node, and role.
	CacheResponses *prometheus.CounterVec
	// BytesServed splits bytes by where they ultimately came from
	// (source=edge for a local cache hit, source=origin otherwise) so
	// the dashboard can compute hit-ratio BY BYTES, not just by request.
	BytesServed *prometheus.CounterVec
	// UpstreamRequests counts fetches this node made to its upstream
	// (a PoP -> shield|origin, the shield -> origin).
	UpstreamRequests *prometheus.CounterVec
	// CacheEntries is the live number of distinct cache entries held by
	// this node - the cardinality that cache-key fragmentation blows up.
	CacheEntries *prometheus.GaugeVec
	// Setting exports the active runtime config so the dashboard can
	// label which run is which (ttl, collapsing, stale_if_error, shield).
	Setting *prometheus.GaugeVec
	// Mode exports the active enum settings as a 1-hot gauge
	// (cache_key_mode / personalized_mode), value 1 on the active value.
	Mode *prometheus.GaugeVec
}

func NewEdge() *Edge {
	return &Edge{
		CacheResponses: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab62_cache_responses_total",
			Help: "Proxied responses by cache status (HIT/MISS/EXPIRED/STALE/BYPASS), node, and role.",
		}, []string{"node", "role", "status"}),
		BytesServed: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab62_bytes_served_total",
			Help: "Response bytes served, split by source (edge=local hit, origin=fetched).",
		}, []string{"node", "source"}),
		UpstreamRequests: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab62_upstream_requests_total",
			Help: "Fetches this node made to its upstream (upstream=shield|origin).",
		}, []string{"node", "upstream"}),
		CacheEntries: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab62_cache_entries",
			Help: "Live count of distinct cache entries held by this node.",
		}, []string{"node"}),
		Setting: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab62_proxy_setting",
			Help: "Numeric runtime settings (setting=ttl_seconds|request_collapsing|stale_if_error|shield_routing).",
		}, []string{"node", "setting"}),
		Mode: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab62_proxy_mode",
			Help: "1-hot enum settings (kind=cache_key|personalized), value=1 on the active mode.",
		}, []string{"node", "kind", "value"}),
	}
}

// Origin holds the metrics exported by the origin so offload and
// origin fan-in (thundering herd) can be measured at the true source.
type Origin struct {
	// Requests counts origin hits by request class.
	Requests *prometheus.CounterVec
	// ObjectRequests counts origin hits per catalog object - bounded
	// cardinality (the catalog is small) - so analyze-fanin can read
	// the per-object origin fetch count around a popular-object expiry.
	ObjectRequests *prometheus.CounterVec
	// OutageActive is 1 while origin outage injection is on.
	OutageActive prometheus.Gauge
	// SetCookieStatic is 1 while the "Set-Cookie on static" misconfig
	// injection is on.
	SetCookieStatic prometheus.Gauge
}

func NewOrigin() *Origin {
	return &Origin{
		Requests: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab62_origin_requests_total",
			Help: "Requests that reached the origin, by class (static/page/api/account).",
		}, []string{"class"}),
		ObjectRequests: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab62_origin_object_requests_total",
			Help: "Origin fetches per catalog object (for thundering-herd fan-in analysis).",
		}, []string{"object"}),
		OutageActive: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab62_origin_outage_active",
			Help: "1 while origin outage injection is active.",
		}),
		SetCookieStatic: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "lab62_origin_setcookie_static_active",
			Help: "1 while Set-Cookie-on-static injection is active.",
		}),
	}
}

// Loadgen exports offered/served counts and the cross-user leak probe
// result. The dashboard reads offered-vs-served from one source.
type Loadgen struct {
	OfferedTotal   *prometheus.CounterVec
	ServedTotal    *prometheus.CounterVec
	RequestSeconds *prometheus.HistogramVec
	// CrossUserLeak counts probe responses personalized for a DIFFERENT
	// user than the one that issued the request (the data breach).
	CrossUserLeak prometheus.Counter
	// ProbeTotal counts every cross-user probe response by result.
	ProbeTotal *prometheus.CounterVec
	RateRPS    *prometheus.GaugeVec
}

func NewLoadgen() *Loadgen {
	lbls := []string{"label", "class"}
	return &Loadgen{
		OfferedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab62_loadgen_offered_total",
			Help: "Requests the loadgen intended to send (offered), by label and class.",
		}, lbls),
		ServedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab62_loadgen_served_total",
			Help: "Requests that completed with a 2xx, by label and class.",
		}, lbls),
		RequestSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lab62_loadgen_request_duration_seconds",
			Help:    "Loadgen-observed end-to-end latency.",
			Buckets: latencyBuckets,
		}, []string{"label", "code"}),
		CrossUserLeak: promauto.NewCounter(prometheus.CounterOpts{
			Name: "lab62_cross_user_leak_total",
			Help: "Cross-user content leaks observed by the probe (response personalized for a different user).",
		}),
		ProbeTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "lab62_loadgen_probe_total",
			Help: "Cross-user probe responses by result (leak|clean).",
		}, []string{"label", "result"}),
		RateRPS: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "lab62_loadgen_rate_rps",
			Help: "Configured target arrival rate (RPS) for the current run.",
		}, []string{"label"}),
	}
}
