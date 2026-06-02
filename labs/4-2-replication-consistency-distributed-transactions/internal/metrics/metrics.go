// Package metrics is a tiny Prometheus helper layer shared by every
// cmd/* binary in this lab. We keep it intentionally thin: the lab is
// about consistency mechanics, not about observability frameworks.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is the global registry every binary registers metrics on.
var Registry = prometheus.NewRegistry()

// Handler returns the standard /metrics handler bound to the lab
// registry.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}

// MustRegister panics if Prometheus refuses the metric. Used at
// process init only.
func MustRegister(cs ...prometheus.Collector) {
	for _, c := range cs {
		Registry.MustRegister(c)
	}
}
