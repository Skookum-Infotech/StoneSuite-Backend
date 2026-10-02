package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// docExtractStageBuckets span a cache-hit stage (milliseconds) to a cold-start
// LLM fallback (about a minute).
var docExtractStageBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}

// docExtractTokenBuckets cover the fallback prompt budget (default 2,500) and
// its short JSON completion.
var docExtractTokenBuckets = []float64{50, 100, 250, 500, 1000, 1500, 2000, 2500, 4000}

var (
	docExtractJobsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "doc_extract_jobs_total",
		Help: "Document-extraction jobs by outcome (success|dead|retry|skipped) and method (parser|parser+llm|cache|none).",
	}, []string{"outcome", "method"})

	docExtractLLMCallsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "doc_extract_llm_calls_total",
		Help: "LLM fallback calls made while extracting documents. Fallback rate = this / doc_extract_jobs_total.",
	})

	docExtractStageSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "doc_extract_stage_seconds",
		Help:    "Latency of one document-extraction pipeline stage (download|extract|resolve|persist).",
		Buckets: docExtractStageBuckets,
	}, []string{"stage"})

	docExtractTokens = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "doc_extract_tokens",
		Help:    "Tokens the fallback model processed for one document, by kind (prompt|completion).",
		Buckets: docExtractTokenBuckets,
	}, []string{"kind"})
)

// ObserveDocExtractJob counts one finished document-extraction job.
func ObserveDocExtractJob(outcome, method string) {
	docExtractJobsTotal.WithLabelValues(outcome, method).Inc()
}

// ObserveDocExtractLLMCall counts one LLM fallback call.
func ObserveDocExtractLLMCall() { docExtractLLMCallsTotal.Inc() }

// ObserveDocExtractStage records the latency of one pipeline stage.
func ObserveDocExtractStage(stage string, seconds float64) {
	docExtractStageSeconds.WithLabelValues(stage).Observe(seconds)
}

// ObserveDocExtractTokens records the tokens one document used on the fallback model.
func ObserveDocExtractTokens(promptTokens, completionTokens int) {
	docExtractTokens.WithLabelValues("prompt").Observe(float64(promptTokens))
	docExtractTokens.WithLabelValues("completion").Observe(float64(completionTokens))
}
