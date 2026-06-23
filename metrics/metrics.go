// Package metrics exposes Prometheus instrumentation for the HTTP layer.
//
// It registers two HTTP collectors plus the default Go runtime/process
// collectors (via promhttp), and is scraped at /api/metrics. Fly.io has a
// built-in Prometheus + Grafana that scrapes this for free, so no external
// metrics backend is required.
package metrics

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests by method, normalized route, and status code.",
	}, []string{"method", "route", "status"})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency in seconds by method and normalized route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
)

// Observe records one completed HTTP request. route should already be
// normalized (see NormalizeRoute) to keep label cardinality bounded.
func Observe(method, route string, status int, seconds float64) {
	requestsTotal.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	requestDuration.WithLabelValues(method, route).Observe(seconds)
}

// Handler returns the Prometheus exposition handler (includes Go runtime and
// process collectors registered on the default registry).
func Handler() http.Handler { return promhttp.Handler() }

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// isIDSegment reports whether a path segment is a per-record identifier that
// would explode metric cardinality if used as a label (UUIDs, numeric ids,
// long opaque tokens). Such segments are collapsed to a placeholder.
func isIDSegment(s string) bool {
	if s == "" {
		return false
	}
	if uuidRe.MatchString(s) {
		return true
	}
	allDigits := true
	for _, r := range s {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return true
	}
	// Long opaque values (tokens, hex blobs) — treat as ids.
	return len(s) >= 24
}

// NormalizeRoute collapses id-like path segments to "{id}" so that, e.g.,
// /api/tenant/records/9f3c... and /api/tenant/records/abcd... map to the single
// series /api/tenant/records/{id}. This bounds the route label's cardinality.
func NormalizeRoute(path string) string {
	if path == "" {
		return "/"
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if isIDSegment(s) {
			segs[i] = "{id}"
		}
	}
	return strings.Join(segs, "/")
}
