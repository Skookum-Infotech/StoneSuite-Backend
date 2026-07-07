package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	aiEmbedDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ai_embed_duration_seconds",
		Help:    "Latency of the query-embedding step of an /ai/ask request.",
		Buckets: prometheus.DefBuckets,
	})

	aiLLMDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ai_llm_duration_seconds",
		Help:    "Latency of the chat-completion step of an /ai/ask request.",
		Buckets: prometheus.DefBuckets,
	})

	aiLLMTimeoutsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ai_llm_timeouts_total",
		Help: "Chat completions that failed due to the LLM client's own deadline, not a downstream error.",
	})

	aiAsksTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ai_asks_total",
		Help: "Total AI assistant asks that reached a chat completion (excludes the analytical count fast-path).",
	})

	// aiRefusalsTotal is the numerator of the assistant's refusal rate — the
	// share of asks where the model said "I don't have that information"
	// instead of answering. The primary answer-quality signal called for in
	// the RAG architecture review: watch this alongside deploys that touch
	// retrieval (P0.2/P0.4), the chat model (P0.3), or grounding content.
	aiRefusalsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ai_refusals_total",
		Help: `Asks where the assistant answered "I don't have that information." Refusal rate = this / ai_asks_total.`,
	})

	ragIndexQueuePending = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rag_index_queue_pending",
		Help: "Number of rag_index_queue jobs still pending, by tenant.",
	}, []string{"tenant"})

	ragIndexQueueOldestPendingAge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rag_index_queue_oldest_pending_age_seconds",
		Help: "Age in seconds of the oldest pending rag_index_queue job, by tenant. 0 when nothing is pending.",
	}, []string{"tenant"})
)

// ObserveAIEmbed records the query-embedding step's latency for one ask.
func ObserveAIEmbed(seconds float64) { aiEmbedDuration.Observe(seconds) }

// ObserveAILLM records the chat-completion step's latency for one ask, and
// whether it failed because the LLM client's own deadline elapsed (as
// opposed to some other error) — see ai.Orchestrator.Ask.
func ObserveAILLM(seconds float64, timedOut bool) {
	aiLLMDuration.Observe(seconds)
	if timedOut {
		aiLLMTimeoutsTotal.Inc()
	}
}

// ObserveAIAsk records one completed ask that reached the chat model, and
// whether the model refused (answered "I don't have that information").
func ObserveAIAsk(refused bool) {
	aiAsksTotal.Inc()
	if refused {
		aiRefusalsTotal.Inc()
	}
}

// SetRAGIndexQueueStats publishes one tenant's rag_index_queue backlog —
// pending job count and the oldest pending job's age — so an operator can
// see whether indexing is falling behind (ai/index.Queue.Stats).
func SetRAGIndexQueueStats(tenant string, pending int, oldestPendingAgeSeconds float64) {
	ragIndexQueuePending.WithLabelValues(tenant).Set(float64(pending))
	ragIndexQueueOldestPendingAge.WithLabelValues(tenant).Set(oldestPendingAgeSeconds)
}

// AI is a Prometheus-backed implementation of ai.Metrics, satisfied
// structurally — this package deliberately doesn't import ai (nothing in it
// is app-specific), so there's no import-cycle risk. Wire it in at the
// construction site that imports both packages, e.g.:
//
//	orch := ai.NewOrchestrator(emb, ret, llm).WithMetrics(metrics.AI{})
type AI struct{}

// ObserveEmbed implements ai.Metrics.
func (AI) ObserveEmbed(seconds float64) { ObserveAIEmbed(seconds) }

// ObserveLLM implements ai.Metrics.
func (AI) ObserveLLM(seconds float64, timedOut bool) { ObserveAILLM(seconds, timedOut) }

// ObserveAsk implements ai.Metrics.
func (AI) ObserveAsk(refused bool) { ObserveAIAsk(refused) }
