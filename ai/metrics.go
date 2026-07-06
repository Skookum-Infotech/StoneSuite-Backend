package ai

// Metrics receives AI pipeline instrumentation from Orchestrator.Ask. Defined
// at the consumer (this package), not the implementation — a real
// Prometheus-backed sink lives in the metrics package and is wired in via
// Orchestrator.WithMetrics; an Orchestrator built without that call gets a
// working no-op for free, so this stays optional and every existing caller
// (including tests) is unaffected.
type Metrics interface {
	// ObserveEmbed records the query-embedding step's latency.
	ObserveEmbed(seconds float64)
	// ObserveLLM records the chat-completion step's latency, and whether it
	// failed because the LLM client's own deadline elapsed.
	ObserveLLM(seconds float64, timedOut bool)
	// ObserveAsk records one completed ask that reached the chat model, and
	// whether the model refused to answer.
	ObserveAsk(refused bool)
}

// noopMetrics discards everything — the default when no Metrics is wired.
type noopMetrics struct{}

func (noopMetrics) ObserveEmbed(float64)     {}
func (noopMetrics) ObserveLLM(float64, bool) {}
func (noopMetrics) ObserveAsk(bool)          {}
