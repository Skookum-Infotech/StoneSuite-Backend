package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/metrics"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
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
	routeCountDirect = "count_direct"
	routeCountRouted = "count_routed"
	routeRAG         = "rag"
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

// conversationTitleFromQuestion derives a conversation's title from its first
// question: trimmed and capped at maxConversationTitleLength characters (not
// bytes — cutting bytes can split a multi-byte character), with "..." if cut.
func conversationTitleFromQuestion(question string) string {
	q := []rune(strings.TrimSpace(question))
	if len(q) <= maxConversationTitleLength {
		return string(q)
	}
	return strings.TrimSpace(string(q[:maxConversationTitleLength])) + "..."
}

// preparedAsk is everything Ask and AskStream both need after their shared
// security chain has run.
type preparedAsk struct {
	tenant *tenancy.Tenant
	pool   *pgxpool.Pool
	// grants is the caller's read scope per CRM record type. Empty means
	// help-docs-only: no record retrieval, and counts answer "no access".
	grants ai.Grants
	// identityID is payload.ID, the JWT/auth identity — what the count-query
	// helpers expect as actorIdentityID.
	identityID string
	// callerUserID is the resolved internal users.id — the RAG scope key
	// (rag_chunks.owner_user_id) and what a conversation row is owned by.
	callerUserID string
	convStore    *ai.ConversationStore
	conv         *ai.Conversation
	history      []ragcore.Message
}

// mode names what the caller can search, for logs.
func (pa preparedAsk) mode() string {
	if len(pa.grants.Types()) == 0 {
		return "help_only"
	}
	return "records"
}

// prepareAsk runs the full chain Ask and AskStream both require before any
// retrieval or LLM work happens: caller auth, tenant/pool resolution, body
// decode + question validation, per-type RBAC grants, caller-user-id
// resolution, and — if the request named a conversation — 404-not-403
// ownership verification plus history load, in that order. Auth is checked
// before the body is decoded, so an unauthenticated request with a malformed
// body still surfaces as 401, not 400.
//
// A caller with no CRM read grant at all is NOT refused: they get
// help-docs-only answers (how to use StoneSuite), since those contain nobody's
// data.
//
// Extracted so the two endpoints cannot drift on this security chain. Returns
// ok=false after already writing the JSON error response. For AskStream,
// everything here must finish before the first SSE byte.
func (h *AIOps) prepareAsk(w http.ResponseWriter, r *http.Request) (askRequestBody, preparedAsk, bool) {
	var body askRequestBody
	var pa preparedAsk

	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return body, pa, false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return body, pa, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return body, pa, false
	}

	status, err := h.aiSettings.Status(r.Context(), h.cpPool, pool, tenant.ID)
	if err != nil {
		slog.Error("ai settings status check failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", tenant.ID, "err", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return body, pa, false
	}
	if !status.Available {
		writeAssistantDisabled(w)
		return body, pa, false
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAskBodyBytes)).Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusRequestEntityTooLarge, "Request is too large.")
			return body, pa, false
		}
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return body, pa, false
	}
	if strings.TrimSpace(body.Question) == "" {
		fail(w, http.StatusBadRequest, "question is required.")
		return body, pa, false
	}
	if len(body.Question) > maxQuestionLength {
		fail(w, http.StatusBadRequest, "question is too long, please shorten it.")
		return body, pa, false
	}

	grants, err := resolveAIGrants(r.Context(), pool, payload.ID)
	if err != nil {
		slog.Error("ai grant check failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", tenant.ID, "err", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return body, pa, false
	}

	callerID, ok := callerUserID(w, r, pool)
	if !ok {
		return body, pa, false
	}

	// Conversation resolution: 404 (never 403) on any owner mismatch or
	// missing id, the same IDOR-safe convention as recordInScope elsewhere —
	// a conversation is personal chat history keyed on callerUserID, not RBAC
	// scope, and its existence must not be distinguishable from "not yours".
	convStore := ai.NewConversationStore(pool)
	var conv *ai.Conversation
	var history []ragcore.Message
	if body.ConversationID != "" {
		c, found, err := convStore.Get(r.Context(), body.ConversationID)
		if err != nil {
			slog.Error("ai conversation lookup failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", tenant.ID, "err", err)
			fail(w, http.StatusInternalServerError, "Failed to load conversation.")
			return body, pa, false
		}
		if !found || c.OwnerUserID != callerID {
			logSecurityEvent(r, "idor_denied", "identity", payload.ID, "conversation", body.ConversationID)
			fail(w, http.StatusNotFound, "Conversation not found.")
			return body, pa, false
		}
		conv = &c
		history, err = convStore.History(r.Context(), conv.ID)
		if err != nil {
			slog.Error("ai conversation history load failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", tenant.ID, "err", err)
			fail(w, http.StatusInternalServerError, "Failed to load conversation history.")
			return body, pa, false
		}
	}

	return body, preparedAsk{
		tenant: tenant, pool: pool, grants: grants,
		identityID: payload.ID, callerUserID: callerID,
		convStore: convStore, conv: conv, history: history,
	}, true
}

// recordTurn persists one question/answer turn onto pa.conv, if there is one,
// and reports whether it was saved. Called only once an ask actually succeeds
// (for AskStream: only on "done" — a truncated assistant message would poison
// every later turn that replays it as context). A save failure doesn't fail
// the ask the user already watched complete; it is logged and surfaced to the
// client as persisted=false.
//
// Deliberately NOT r.Context(): this runs after a slow completion, and a
// caller who navigated away has already had their request context cancelled.
// WithoutCancel keeps the pool and values and drops only the cancellation; the
// short timeout stops a wedged write outliving the request by more than a
// moment.
func recordTurn(r *http.Request, pa preparedAsk, question, answer string) bool {
	if pa.conv == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := pa.convStore.AppendTurn(ctx, pa.conv.ID, question, answer, conversationTitleFromQuestion(question)); err != nil {
		slog.Error("failed to record ai conversation turn", "request_id", middleware.RequestIDFromContext(ctx), "tenant_id", pa.tenant.ID, "err", err)
		return false
	}
	return true
}

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

	if needsModel(body.Question) {
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
		status, outcome, msg := classifyAIError(err)
		slog.Error("ai ask failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", pa.tenant.ID, "route", route, "err", err)
		finishAsk(r, pa, endpointAsk, route, outcome, start)
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", strconv.Itoa(aiBusyRetryAfter))
		}
		fail(w, status, msg)
		return
	}

	observeSuccess(route, res)
	persisted := recordTurn(r, pa, body.Question, res.Answer)
	finishAsk(r, pa, endpointAsk, route, outcomeOK, start)

	resp := map[string]any{"success": true, "data": map[string]any{
		"answer":    res.Answer,
		"citations": citationDTOs(ctx, pa.pool, res.Citations),
		"truncated": res.Usage.Truncated,
	}}
	if pa.conv != nil {
		resp["conversation_id"] = pa.conv.ID
		resp["persisted"] = persisted
	}
	writeJSON(w, http.StatusOK, resp)
}
