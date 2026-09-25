package controllers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"stonesuite-backend/crmstore"
	"stonesuite-backend/metrics"
	"stonesuite-backend/middleware"
)

type askRequestBody struct {
	Question string `json:"question"`
	// ConversationID is optional. When set, the ask loads that conversation's
	// history into the prompt and records this turn onto it (404 if the
	// conversation doesn't exist or isn't the caller's own — see
	// ConversationOps for how one is created). Omitted or empty: a single-turn
	// ask, no history, nothing recorded.
	ConversationID string `json:"conversation_id"`
}

// maxQuestionLength keeps the question comfortably under the embedder's
// ~512-token context window (AI_EMBED_MODEL, see docs/ai-assistant.md) so a
// long question fails fast with a clear message instead of a generic 502
// from Ollama's "input length exceeds the context length" error. Bytes, not
// characters — the frontend enforces the same byte limit.
const maxQuestionLength = 2000

// maxAskBodyBytes caps the request body before decoding: a 2000-byte question
// plus a conversation id and JSON framing fits many times over.
const maxAskBodyBytes = 16 << 10

// maxConversationTitleLength bounds the auto-generated title set from a
// conversation's first question — long enough to be recognizable in a list,
// short enough to never need its own wrapping/truncation in a UI.
const maxConversationTitleLength = 80

// askMaxDuration bounds a non-streaming ask end to end. Routing (up to one
// model call), embedding, and generation could together exceed the server's
// 120s WriteTimeout, which drops the connection with no response body at
// all; failing at 110s returns a real error the client can show.
const askMaxDuration = 110 * time.Second

// Endpoint labels for metrics.
const (
	endpointAsk    = "ask"
	endpointStream = "stream"
)

// Dispatch routes.
const (
	routeCountDirect   = "count_direct"
	routeCountRouted   = "count_routed"
	routeCountFollowUp = "count_followup"
	routeLookup        = "lookup"
	routeHelpCache     = "help_cache"
	routeRAG           = "rag"
)

// Request outcomes for metrics and the ai_query security log.
const (
	outcomeOK           = "ok"
	outcomeError        = "error"
	outcomeTimeout      = "timeout"
	outcomeUnavailable  = "unavailable"
	outcomeBusy         = "busy"
	outcomeDisconnected = "disconnected"
)

// acquireSlot waits up to aiSlotWait for a model slot, recording the wait.
func (h *AIOps) acquireSlot(ctx context.Context, tenantID string) (func(), bool) {
	waitCtx, cancel := context.WithTimeout(ctx, h.slotWait)
	defer cancel()
	start := time.Now()
	release, ok := h.slots.acquire(waitCtx, tenantID)
	metrics.ObserveAISlotWait(time.Since(start).Seconds())
	return release, ok
}

// Ask handles POST /api/tenant/ai/ask — the non-streaming twin of AskStream,
// sharing its security chain (prepareAsk), dispatch (runAskDispatch), and
// model-slot limiter.
func (h *AIOps) Ask(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, pa, ok := h.prepareAsk(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), askMaxDuration)
	defer cancel()

	if needsModel(body.Question, pa.history) {
		release, ok := h.acquireSlot(ctx, pa.tenant.ID)
		if !ok {
			finishAsk(r, pa, endpointAsk, routeRAG, outcomeBusy, start)
			writeAssistantBusy(w)
			return
		}
		defer release()
	}

	store := crmstore.For(pa.tenant.DesignVersion)
	res, route, err := runAskDispatch(ctx, h, pa, store, body.Question, nil)
	if err != nil {
		status, outcome, code, msg := classifyAIError(err)
		slog.Error("ai ask failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", pa.tenant.ID, "route", route, "err", err)
		finishAsk(r, pa, endpointAsk, route, outcome, start)
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", strconv.Itoa(aiBusyRetryAfter))
		}
		failWithCode(w, status, msg, code)
		return
	}

	observeSuccess(route, res)
	persisted := recordTurn(r, pa, body.Question, res.Answer)
	finishAsk(r, pa, endpointAsk, route, outcomeOK, start)

	resp := map[string]any{"success": true, "data": map[string]any{
		"answer":    res.Answer,
		"citations": citationDTOs(ctx, pa.pool, res.Citations),
		"truncated": res.Usage.Truncated,
		"route":     route,
	}}
	if pa.conv != nil {
		resp["conversation_id"] = pa.conv.ID
		resp["persisted"] = persisted
	}
	writeJSON(w, http.StatusOK, resp)
}
