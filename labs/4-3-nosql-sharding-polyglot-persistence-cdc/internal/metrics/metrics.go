// Package metrics is the tiny shared Prometheus registry every binary in
// this lab registers metrics on. Mirrors lab 4-2's package shape.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var Registry = prometheus.NewRegistry()

func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}

func MustRegister(cs ...prometheus.Collector) {
	for _, c := range cs {
		Registry.MustRegister(c)
	}
}
