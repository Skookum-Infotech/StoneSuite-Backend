package controllers

import (
	"context"

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
	if sink == nil {
		res, err := assistant.Ask(ctx, req)
		return res, routeRAG, err
	}
	res, err := assistant.AskStream(ctx, req, sink)
	return res, routeRAG, err
}
