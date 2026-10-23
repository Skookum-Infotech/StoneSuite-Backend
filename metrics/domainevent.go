package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	domainEventHandlerFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "domain_event_handler_failures_total",
		Help: "Domain-event handler invocations that returned an error or panicked, by consumer.",
	}, []string{"consumer"})

	domainEventDeadLettered = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "domain_event_dead_lettered_total",
		Help: "Domain events skipped after exhausting retry attempts, by consumer.",
	}, []string{"consumer"})
)

// DomainEventHandlerFailed counts one failed domain-event handler attempt.
func DomainEventHandlerFailed(consumer string) {
	domainEventHandlerFailures.WithLabelValues(consumer).Inc()
}

// DomainEventDeadLettered counts one domain event skipped after max attempts.
func DomainEventDeadLettered(consumer string) {
	domainEventDeadLettered.WithLabelValues(consumer).Inc()
}
