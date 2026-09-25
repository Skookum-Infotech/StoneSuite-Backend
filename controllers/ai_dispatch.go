package controllers

import (
	"context"
	"errors"
	"log/slog"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/metrics"
)

// needsModel reports whether answering question may call the chat model —
// everything except the zero-LLM direct count. Decided before dispatch so a
// model slot is only ever held by requests that can use one.
func needsModel(question string) bool {
	_, direct := classifyCountQuestion(question)
	return !direct
}

// runAskDispatch is the one three-way dispatch both endpoints share (direct
// count / LLM-routed filtered count / RAG), so they can never answer the same
// question differently. sink is nil for the non-streaming endpoint. The count
// paths compute their whole answer synchronously and, when streaming, deliver
// it as one "sources" event (no citations) plus one "token" event — giving the
// client exactly one rendering path regardless of route.
func runAskDispatch(ctx context.Context, h *AIOps, pa preparedAsk, store crmstore.Store, question string, sink ragcore.StreamSink) (ragcore.AskResult, string, error) {
	emit := func(res ragcore.AskResult) error {
		if sink == nil {
			return nil
		}
		if err := sink.OnRetrieved(res.Citations); err != nil {
			return err
		}
		return sink.OnToken(res.Answer)
	}

	if keys, matched := classifyCountQuestion(question); matched {
		res, err := countCRMRecords(ctx, store, pa.pool, pa.grants, pa.identityID, keys)
		if err != nil {
			return res, routeCountDirect, err
		}
		return res, routeCountDirect, emit(res)
	}

	if hasFilterHintCountIntent(question) {
		if routed, ok := resolveRoutedFilteredCount(ctx, h.llm, store, pa.pool, pa.grants, pa.identityID, question, pa.history); ok {
			return routed, routeCountRouted, emit(routed)
		}
	}

	assistant := ai.NewAssistant(pa.pool, h.cpPool, h.queryEmbed, h.llm).WithMetrics(metrics.AI{})
	if h.reranker != nil {
		assistant = assistant.WithReranker(h.reranker, h.rerankCandidates)
	}
	req := ai.AskRequest{Question: question, Grants: pa.grants, CallerUserID: pa.callerUserID, History: pa.history}
	// Both endpoints go through AskStream so the retrieved set is always
	// observable (Ask only returns the cited subset); the non-streaming caller
	// just discards the tokens and gets the assembled answer back.
	collector := &retrievedCollector{next: sink}
	res, err := h.withOllamaWake(ctx, collector, func(s ragcore.StreamSink) (ragcore.AskResult, error) {
		return assistant.AskStream(ctx, req, s)
	})
	if err == nil {
		res.Citations = withRetrievedFallback(res, collector.retrieved)
	}
	return res, routeRAG, err
}

// maxFallbackCitations bounds how many retrieved sources are attached to an
// answer that cited none.
const maxFallbackCitations = 3

// withRetrievedFallback returns the citations to send. The library keeps only
// sources the answer references by a [n] marker, and the small self-hosted
// model often writes none — leaving a grounded answer with no sources at all.
// In that case the top retrieved sources are attached instead. A refusal
// (not grounded) never gets sources.
func withRetrievedFallback(res ragcore.AskResult, retrieved []ragcore.Citation) []ragcore.Citation {
	if !res.Grounded || len(res.Citations) > 0 || len(retrieved) == 0 {
		return res.Citations
	}
	if len(retrieved) > maxFallbackCitations {
		retrieved = retrieved[:maxFallbackCitations]
	}
	return retrieved
}

// retrievedCollector remembers the retrieved set and forwards to next when
// there is one (nil for the non-streaming endpoint, where tokens are dropped).
type retrievedCollector struct {
	next      ragcore.StreamSink
	retrieved []ragcore.Citation
}

func (c *retrievedCollector) OnRetrieved(cites []ragcore.Citation) error {
	c.retrieved = cites
	if c.next == nil {
		return nil
	}
	return c.next.OnRetrieved(cites)
}

func (c *retrievedCollector) OnToken(tok string) error {
	if c.next == nil {
		return nil
	}
	return c.next.OnToken(tok)
}

// ollamaWaker is the point-of-use view of services.OllamaWaker.
type ollamaWaker interface {
	EnsureRunning(ctx context.Context) error
}

// withOllamaWake runs attempt and, if the model box turns out to be down
// (ragcore.ErrUnavailable), starts it and runs attempt once more. The provider
// only reports ErrUnavailable before the first token, so a retry never repeats
// text the client already has; the retry's "sources" event is swallowed for the
// same reason. sink may be nil, in which case attempt gets nil.
func (h *AIOps) withOllamaWake(ctx context.Context, sink ragcore.StreamSink, attempt func(ragcore.StreamSink) (ragcore.AskResult, error)) (ragcore.AskResult, error) {
	var guarded *retrySafeSink
	var s ragcore.StreamSink
	if sink != nil {
		guarded = &retrySafeSink{StreamSink: sink}
		s = guarded
	}
	res, err := attempt(s)
	if err == nil || h.waker == nil || !errors.Is(err, ragcore.ErrUnavailable) || (guarded != nil && guarded.tokens > 0) {
		return res, err
	}
	start := time.Now()
	if werr := h.waker.EnsureRunning(ctx); werr != nil {
		slog.Warn("ai: ollama unavailable and on-demand start failed", "error", werr)
		return res, err
	}
	slog.Info("ai: ollama started on demand", "waited_ms", time.Since(start).Milliseconds())
	return attempt(s)
}

// retrySafeSink forwards to the real sink but delivers "sources" only once and
// counts tokens, so withOllamaWake can tell whether a retry is still safe.
type retrySafeSink struct {
	ragcore.StreamSink
	retrieved bool
	tokens    int
}

func (r *retrySafeSink) OnRetrieved(cites []ragcore.Citation) error {
	if r.retrieved {
		return nil
	}
	r.retrieved = true
	return r.StreamSink.OnRetrieved(cites)
}

func (r *retrySafeSink) OnToken(tok string) error {
	r.tokens++
	return r.StreamSink.OnToken(tok)
}
