package controllers

import (
	"context"
	"errors"
	"log/slog"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/config"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/docs"
	"stonesuite-backend/metrics"
)

// needsModel reports whether answering question may call the chat model —
// everything except a zero-LLM path (direct count, a count follow-up, or a
// record-number lookup that resolves to a template answer). Decided before
// dispatch so a model slot is only ever held by requests that can use one.
// history is pa.history — the count-follow-up path needs the previous turn
// to even recognize itself.
func needsModel(question string, history []ragcore.Message) bool {
	if _, direct := classifyCountQuestion(question); direct {
		return false
	}
	if _, ok := classifyFollowUpCount(question, history); ok {
		return false
	}
	if _, hasField, ok := lookupTemplateMatch(question); ok && hasField {
		return false
	}
	return true
}

// runAskDispatch is the dispatch both endpoints share (record lookup / direct
// count / count follow-up / LLM-routed filtered count / help-cache / RAG), so
// they can never answer the same question differently. sink is nil for the
// non-streaming endpoint. Every zero-LLM path computes its whole answer
// synchronously and, when streaming, delivers it as one "sources" event (no
// citations) plus one "token" event — giving the client exactly one
// rendering path regardless of route.
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

	if res, ok := lookupRecordAnswer(ctx, store, pa.pool, pa.grants, pa.identityID, question); ok {
		return res, routeLookup, emit(res)
	}

	if keys, matched := classifyCountQuestion(question); matched {
		res, err := countCRMRecords(ctx, store, pa.pool, pa.grants, pa.identityID, keys)
		if err != nil {
			return res, routeCountDirect, err
		}
		return res, routeCountDirect, emit(res)
	}

	if keys, ok := classifyFollowUpCount(question, pa.history); ok {
		res, err := countCRMRecords(ctx, store, pa.pool, pa.grants, pa.identityID, keys)
		if err != nil {
			return res, routeCountFollowUp, err
		}
		return res, routeCountFollowUp, emit(res)
	}

	if hasFilterHintCountIntent(question) {
		if routed, ok := resolveRoutedFilteredCount(ctx, h.llm, store, pa.pool, pa.grants, pa.identityID, question, pa.history); ok {
			return routed, routeCountRouted, emit(routed)
		}
	}

	intent := classifyIntent(question, previousUserQuestion(pa.history))
	firstTurn := len(pa.history) == 0
	if intent == ai.IntentHelp && firstTurn {
		if res, ok := h.helpCacheGet(question); ok {
			return res, routeHelpCache, emit(res)
		}
	}

	assistant := ai.NewAssistant(pa.pool, h.cpPool, h.queryEmbed, h.llm).WithMetrics(metrics.AI{})
	if h.reranker != nil {
		assistant = assistant.WithReranker(h.reranker, h.rerankCandidates)
	}
	req := ai.AskRequest{Question: question, Grants: pa.grants, CallerUserID: pa.callerUserID, History: pa.history, Intent: intent}
	// Both endpoints go through AskStream so the retrieved set is always
	// observable (Ask only returns the cited subset); the non-streaming caller
	// just discards the tokens and gets the assembled answer back.
	collector := &retrievedCollector{next: sink}
	res, err := h.withOllamaWake(ctx, collector, func(s ragcore.StreamSink) (ragcore.AskResult, error) {
		return assistant.AskStream(ctx, req, s)
	})
	if err == nil {
		res.Citations = withRetrievedFallback(res, collector.retrieved)
		if intent == ai.IntentHelp && firstTurn && res.Grounded {
			h.helpCacheSet(question, res)
		}
	}
	return res, routeRAG, err
}

// helpCacheGet looks up question in h.helpCache under today's (docs content
// hash, chat model) — see ai_help_cache.go. A hash failure (embedded FS
// walk/read error, effectively impossible in production) or a nil cache just
// misses rather than failing the ask.
func (h *AIOps) helpCacheGet(question string) (ragcore.AskResult, bool) {
	if h.helpCache == nil {
		return ragcore.AskResult{}, false
	}
	key, ok := h.helpCacheKey(question)
	if !ok {
		return ragcore.AskResult{}, false
	}
	answer, citations, ok := h.helpCache.get(key)
	if !ok {
		return ragcore.AskResult{}, false
	}
	return ragcore.AskResult{Answer: answer, Citations: citations, Grounded: true}, true
}

// helpCacheSet stores res under question's cache key — called only for a
// grounded, first-turn, intentHelp RAG answer (see runAskDispatch).
func (h *AIOps) helpCacheSet(question string, res ragcore.AskResult) {
	if h.helpCache == nil {
		return
	}
	key, ok := h.helpCacheKey(question)
	if !ok {
		return
	}
	h.helpCache.set(key, res.Answer, res.Citations)
}

// helpCacheKey builds the cache key for question: the normalized question,
// the embedded help corpus's content hash (so a doc re-ingest invalidates
// every cached answer), and the configured chat model tag.
func (h *AIOps) helpCacheKey(question string) (helpCacheKey, bool) {
	hash, err := docs.ContentHash()
	if err != nil {
		slog.Warn("ai help cache: docs content hash failed; skipping cache", "err", err)
		return helpCacheKey{}, false
	}
	return helpCacheKey{
		question:  normalizeHelpCacheQuestion(question),
		docsHash:  hash,
		chatModel: config.AppConfig.AIChatModel,
	}, true
}

// maxFallbackCitations bounds how many retrieved sources are attached to an
// answer that cited none.
const maxFallbackCitations = 3

// withRetrievedFallback returns the citations to send. The library keeps only
// sources the answer references by a [n] marker, and the small self-hosted
// model often writes none — leaving a grounded answer with no sources at all.
// In that case the top retrieved sources are attached instead.
//
// !res.Grounded short-circuits to res.Citations unchanged rather than
// substituting retrieved sources — correct because the library itself always
// resets Citations to empty on a refusal (rag.Orchestrator.finalizeAnswer):
// Grounded is a reliable signal here, not a heuristic this function has to
// second-guess by also checking res.Answer against the refusal phrase.
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
	logRetrievedDistances(cites)
	if c.next == nil {
		return nil
	}
	return c.next.OnRetrieved(cites)
}

// logRetrievedDistances logs each retrieved chunk's vector distance at debug
// level — the raw signal behind recordsFloorDist/helpFloorDist (ai/adapter.go)
// so those provisional floors can eventually be calibrated from real traffic
// rather than a guess. A lexical-only match has no valid distance and is
// logged as such rather than a misleading 0.
func logRetrievedDistances(cites []ragcore.Citation) {
	for _, c := range cites {
		if !c.DistanceValid {
			slog.Debug("ai retrieval: chunk distance", "corpus", c.SourceType, "source_id", c.SourceID, "distance", "lexical_only")
			continue
		}
		slog.Debug("ai retrieval: chunk distance", "corpus", c.SourceType, "source_id", c.SourceID, "distance", c.Distance)
	}
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

// wakeOutcome wraps a dispatch error with what withOllamaWake learned about
// an on-demand Ollama start attempt, so classifyAIError can tell "the
// assistant is switched off" from "cooling down after a failed start" from
// "started, but the retry itself timed out" instead of collapsing every wake
// failure into the same generic message. Unwrap returns the underlying
// dispatch error, so existing errors.Is/As checks against it (context
// deadlines, ragcore sentinels) keep working unchanged.
type wakeOutcome struct {
	err error
	// waked is true once EnsureRunning returned nil and attempt was retried —
	// distinguishes "the box came up but the retry itself was slow" from
	// every other outcome.
	waked bool
	// wakeErr is EnsureRunning's own error, non-nil only when a wake was
	// attempted and failed (services.ErrWakeDisabled, services.ErrWakeCooldown,
	// or another OllamaWaker failure).
	wakeErr error
}

func (w *wakeOutcome) Error() string { return w.err.Error() }
func (w *wakeOutcome) Unwrap() error { return w.err }

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
	if err == nil || !errors.Is(err, ragcore.ErrUnavailable) || (guarded != nil && guarded.tokens > 0) {
		return res, err
	}
	if h.waker == nil {
		return res, &wakeOutcome{err: err}
	}
	start := time.Now()
	if werr := h.waker.EnsureRunning(ctx); werr != nil {
		slog.Warn("ai: ollama unavailable and on-demand start failed", "error", werr)
		return res, &wakeOutcome{err: err, wakeErr: werr}
	}
	slog.Info("ai: ollama started on demand", "waited_ms", time.Since(start).Milliseconds())
	res, err = attempt(s)
	if err == nil {
		return res, nil
	}
	return res, &wakeOutcome{err: err, waked: true}
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
