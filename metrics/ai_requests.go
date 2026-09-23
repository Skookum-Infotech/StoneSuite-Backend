package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// aiRequestBuckets span a zero-LLM count (milliseconds) to a full CPU-bound
// generation (up to the ~110s ask deadline).
var aiRequestBuckets = []float64{0.1, 0.5, 1, 2.5, 5, 10, 20, 40, 60, 90, 120}

var (
	aiRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_requests_total",
		Help: "AI assistant requests by endpoint (ask|stream), dispatch route, and outcome (ok|error|timeout|unavailable|busy|disconnected). Unlike ai_query_route_total, failures are counted too.",
	}, []string{"endpoint", "route", "outcome"})

	aiRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ai_request_duration_seconds",
		Help:    "End-to-end latency of an AI assistant request, from slot acquisition to the final response byte.",
		Buckets: aiRequestBuckets,
	}, []string{"endpoint", "route"})

	aiSlotWait = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ai_slot_wait_seconds",
		Help:    "Time an AI request waited for a model slot before generation (0 when one was free).",
		Buckets: []float64{0.01, 0.1, 0.5, 1, 2, 5, 10},
	})

	aiTokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_tokens_total",
		Help: "Tokens processed by the chat model, by kind (prompt|completion).",
	}, []string{"kind"})

	aiTruncatedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ai_truncated_answers_total",
		Help: "Answers cut off by the output-token cap rather than finishing naturally.",
	})
)

// ObserveAIRequest records one finished AI request's outcome and latency.
func ObserveAIRequest(endpoint, route, outcome string, seconds float64) {
	aiRequestsTotal.WithLabelValues(endpoint, route, outcome).Inc()
	aiRequestDuration.WithLabelValues(endpoint, route).Observe(seconds)
}

// ObserveAISlotWait records how long a request waited for a model slot.
func ObserveAISlotWait(seconds float64) { aiSlotWait.Observe(seconds) }

// ObserveAIUsage records what the chat model reported for one generation.
func ObserveAIUsage(promptTokens, completionTokens int, truncated bool) {
	aiTokensTotal.WithLabelValues("prompt").Add(float64(promptTokens))
	aiTokensTotal.WithLabelValues("completion").Add(float64(completionTokens))
	if truncated {
		aiTruncatedTotal.Inc()
	}
}
