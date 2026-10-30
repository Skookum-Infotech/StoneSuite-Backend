package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var goroutinePanics = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "goroutine_panics_total",
	Help: "Panics recovered in background or fan-out goroutines, by call site.",
}, []string{"goroutine"})

// GoroutinePanic counts one panic recovered by safego at the named call site.
func GoroutinePanic(name string) {
	goroutinePanics.WithLabelValues(name).Inc()
}
