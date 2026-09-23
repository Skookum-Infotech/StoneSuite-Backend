package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/ingest"
	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/ai/index"
	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/docs"
	"stonesuite-backend/metrics"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// aiScopeResources are the CRM resources rag_chunks currently covers (see
// crmstore.CRMWorkflowKeys and controllers/crm.go storeFromContext).
var aiScopeResources = []authz.Resource{authz.ResourceLead, authz.ResourceProspect, authz.ResourceCustomer}

// narrowestScope picks the most restrictive scope among a set of granted
// decisions. Retrieval ANDs a single scope clause across every rag_chunks row
// regardless of source workflow, so using the caller's broadest per-resource
// grant would over-expose a workflow they have narrower access to; using the
// narrowest is the safe (if occasionally under-inclusive) choice. A resource
// with no grant is simply excluded, not treated as a reason to deny — e.g. an
// "own" grant on lead still correctly returns zero prospect chunks the caller
// has no grant on at all, since owner_user_id won't match. Returns ("", false)
// only when NONE of the resources are granted.
func narrowestScope(decisions []authz.Decision) (authz.Scope, bool) {
	var narrowest authz.Scope
	granted := false
	for _, d := range decisions {
		if !d.Allowed {
			continue
		}
		if !granted || authz.ScopeRank(d.Scope) < authz.ScopeRank(narrowest) {
			narrowest = d.Scope
			granted = true
		}
	}
	return narrowest, granted
}

// platformAdminChecker is the point-of-use interface ReindexHelp depends on
// for its admin gate — satisfied by *tenancy.ControlPlane. Defined here so
// the gate is testable without a real database.
type platformAdminChecker interface {
	IsPlatformAdmin(ctx context.Context, identityID string) (bool, error)
}

// AIOps serves the tenant AI assistant: POST /api/tenant/ai/ask (RAG chat),
// POST /api/tenant/ai/reindex (admin: re-enqueue every CRM record), and
// POST /api/platform/ai/reindex-help (platform admin: re-embed app-help
// docs). queryEmbed, docEmbed, and llm are injected so tests can substitute
// ai.FakeEmbedder / ai.FakeLLM — no network calls in tests.
type AIOps struct {
	cpPool           *pgxpool.Pool
	queryEmbed       ragcore.Embedder
	docEmbed         ragcore.Embedder
	llm              ragcore.LLMClient
	cp               platformAdminChecker
	reranker         ragcore.Reranker
	rerankCandidates int
	// streamSem bounds concurrent AskStream calls — a buffered channel used
	// as a counting semaphore (send to acquire, receive to release). Streams
	// run for minutes on a 1-CPU self-hosted box, unlike an ordinary request;
	// the per-tenant/per-user token-bucket limits in main.go's aiChain bound
	// request *rate*, not how many can be in flight at once, so this is the
	// backstop against many slow streams pinning every available goroutine.
	streamSem chan struct{}
}

// maxConcurrentStreams is deliberately small: the Ollama box behind every
// stream is 1 CPU, so beyond a couple of concurrent generations, adding more
// only slows all of them down together rather than serving more people.
const maxConcurrentStreams = 2

// NewAIOps constructs the handler group. queryEmbed MUST be a query embedder
// and docEmbed a document embedder (ollama.NewQueryEmbedder /
// ollama.NewDocEmbedder) — the two apply different task prefixes, and mixing
// them produces vectors that are not comparable with the stored ones.
func NewAIOps(cpPool *pgxpool.Pool, queryEmbed ragcore.Embedder, llm ragcore.LLMClient, cp platformAdminChecker, docEmbed ragcore.Embedder) *AIOps {
	return &AIOps{
		cpPool: cpPool, queryEmbed: queryEmbed, llm: llm, cp: cp, docEmbed: docEmbed,
		streamSem: make(chan struct{}, maxConcurrentStreams),
	}
}

// acquireStream reports whether a stream slot was claimed, non-blocking —
// beyond maxConcurrentStreams, AskStream refuses with 429 rather than
// queuing (a queued stream request is still a client staring at a spinner,
// just a more confusing one).
func (h *AIOps) acquireStream() bool {
	select {
	case h.streamSem <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseStream frees the slot acquireStream claimed. Must be called exactly
// once per successful acquireStream, typically via defer.
func (h *AIOps) releaseStream() {
	<-h.streamSem
}

// WithReranker wires an optional cross-encoder reranker (e.g.
// provider/tei.NewReranker) onto every ask this AIOps serves and returns the
// receiver for chaining at construction in main.go. Unset by default —
// reranking is off unless AI_RERANK_BASE_URL is configured.
func (h *AIOps) WithReranker(r ragcore.Reranker, candidateK int) *AIOps {
	h.reranker = r
	h.rerankCandidates = candidateK
	return h
}

type askRequestBody struct {
	Question string `json:"question"`
	// ConversationID is optional. When set, Ask loads that conversation's
	// history into the prompt and records this turn onto it (404 if the
	// conversation doesn't exist or isn't the caller's own — see
	// ConversationOps for how one is created). Omitted or empty: Ask behaves
	// exactly as a single-turn ask always has, no history, nothing recorded.
	ConversationID string `json:"conversation_id"`
}

// maxQuestionLength keeps the question comfortably under the embedder's
// ~512-token context window (AI_EMBED_MODEL, see docs/ai-assistant.md) so a
// long question fails fast with a clear message instead of a generic 502
// from Ollama's "input length exceeds the context length" error.
const maxQuestionLength = 2000

// maxConversationTitleLength bounds the auto-generated title set from a
// conversation's first question — long enough to be recognizable in a list,
// short enough to never need its own wrapping/truncation in a UI.
const maxConversationTitleLength = 80

// conversationTitleFromQuestion derives a conversation's title from its first
// question: trimmed and capped at maxConversationTitleLength, with "..." if
// it was cut.
func conversationTitleFromQuestion(question string) string {
	q := strings.TrimSpace(question)
	if len(q) <= maxConversationTitleLength {
		return q
	}
	return strings.TrimSpace(q[:maxConversationTitleLength]) + "..."
}

// writeAskResult writes one successful ask's response, including
// conversation_id only when the ask was part of a conversation — additive,
// so a caller not using conversations sees exactly the response shape it
// always has.
func writeAskResult(w http.ResponseWriter, res ragcore.AskResult, conv *ai.Conversation) {
	data := map[string]any{"success": true, "data": res}
	if conv != nil {
		data["conversation_id"] = conv.ID
	}
	writeJSON(w, http.StatusOK, data)
}

// preparedAsk is everything Ask and AskStream both need after their shared
// security chain has run: the resolved tenant/pool, the caller's narrowest
// RBAC scope, both flavors of caller identity the downstream call sites
// expect (see the two fields' own docs), and — if the request named one —
// the conversation plus its history.
type preparedAsk struct {
	tenant *tenancy.Tenant
	pool   *pgxpool.Pool
	scope  authz.Scope
	// identityID is payload.ID, the JWT/auth identity — what the count-query
	// helpers (countCRMRecords, resolveRoutedFilteredCount) expect as
	// actorIdentityID. Kept distinct from callerUserID below because that is
	// what those call sites have always taken; unifying the two is a
	// separate change, not this one's to make.
	identityID string
	// callerUserID is the resolved internal users.id — the RAG scope key
	// (rag_chunks.owner_user_id) and what a conversation row is owned by.
	callerUserID string
	convStore    *ai.ConversationStore
	conv         *ai.Conversation
	history      []ragcore.Message
}

// prepareAsk runs the full chain Ask and AskStream both require before any
// retrieval or LLM work happens: caller auth, tenant/pool resolution, body
// decode + question validation, RBAC scope (narrowest grant across
// aiScopeResources), caller-user-id resolution, and — if the request named a
// conversation — 404-not-403 ownership verification plus history load, in
// that order — auth is checked before the body is even decoded, so an
// unauthenticated request with a malformed body still surfaces as 401, not
// 400 (never leak validation detail to a caller who isn't authenticated).
//
// Extracted so a second endpoint (AskStream) cannot reimplement — and
// silently drift from — this security chain. Returns ok=false after already
// writing the appropriate JSON error response; callers must return
// immediately without writing anything of their own. For AskStream in
// particular, everything here must finish before the first SSE byte: an SSE
// response has already committed to 200 once headers are written, so no 4xx
// can be sent after that point.
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

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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

	decisions := make([]authz.Decision, 0, len(aiScopeResources))
	for _, res := range aiScopeResources {
		d, err := authz.Check(r.Context(), pool, payload.ID, res, authz.ActionRead)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return body, pa, false
		}
		decisions = append(decisions, d)
	}
	scope, ok := narrowestScope(decisions)
	if !ok {
		logSecurityEvent(r, "ai_query_denied", "tenant_id", tenant.ID)
		fail(w, http.StatusForbidden, "You do not have permission to query records.")
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
		tenant: tenant, pool: pool, scope: scope,
		identityID: payload.ID, callerUserID: callerID,
		convStore: convStore, conv: conv, history: history,
	}, true
}

// recordTurn persists one question/answer turn onto conv, if there is one —
// a no-op otherwise. Best-effort: a logging failure here must not fail an
// ask that already succeeded, so errors are logged, not returned to the
// caller. Shared by Ask and AskStream, called only once each ask actually
// succeeds (for AskStream: only from the "done" branch — a stream that never
// finishes must leave nothing in history, or a truncated assistant message
// would poison every later turn that replays it as context).
//
// Deliberately NOT r.Context(): this runs after a slow completion, and a
// caller who navigated away (or whose browser gave up) has already had their
// request context cancelled. Persisting on that context fails every write,
// so the turn the user waited for silently never reaches their history.
// WithoutCancel keeps the pool and values, drops only the cancellation; the
// short timeout stops a wedged write outliving the request by more than a
// moment.
func recordTurn(r *http.Request, tenantID string, convStore *ai.ConversationStore, conv *ai.Conversation, question, answer string) {
	if conv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()

	reqID := middleware.RequestIDFromContext(ctx)
	if err := convStore.AppendMessage(ctx, conv.ID, "user", question); err != nil {
		slog.Error("failed to record ai conversation message", "request_id", reqID, "tenant_id", tenantID, "err", err)
	}
	if err := convStore.AppendMessage(ctx, conv.ID, "assistant", answer); err != nil {
		slog.Error("failed to record ai conversation message", "request_id", reqID, "tenant_id", tenantID, "err", err)
	}
	if err := convStore.SetTitleIfEmpty(ctx, conv.ID, conversationTitleFromQuestion(question)); err != nil {
		slog.Error("failed to set ai conversation title", "request_id", reqID, "tenant_id", tenantID, "err", err)
	}
}

// Ask handles POST /api/tenant/ai/ask. Chain: RequireAuth -> per-tenant rate
// limit -> TenantResolver (via tenantChain in main.go). Scope is resolved
// from the caller's roles and enforced by RagStore.SearchScoped (IDOR-safe).
func (h *AIOps) Ask(w http.ResponseWriter, r *http.Request) {
	body, pa, ok := h.prepareAsk(w, r)
	if !ok {
		return
	}

	store := crmstore.For(pa.tenant.DesignVersion)

	if keys, matched := classifyCountQuestion(body.Question); matched {
		res, err := countCRMRecords(r.Context(), store, pa.pool, string(pa.scope), pa.identityID, keys)
		if err != nil {
			slog.Error("ai count query failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", pa.tenant.ID, "err", err)
			fail(w, http.StatusBadGateway, "The assistant is temporarily unavailable.")
			return
		}
		metrics.ObserveAIQueryRoute("count_direct")
		recordTurn(r, pa.tenant.ID, pa.convStore, pa.conv, body.Question, res.Answer)
		logSecurityEvent(r, "ai_query", "tenant_id", pa.tenant.ID)
		writeAskResult(w, res, pa.conv)
		return
	}

	// The three-way dispatch's middle case: a count question the regex path
	// above refuses because it names a filter concept (date/status/etc) it
	// cannot safely honor. One constrained LLM call resolves the filter; a
	// failure or unparseable/adversarial model output falls through to plain
	// RAG below rather than erroring — see resolveRoutedFilteredCount.
	if hasFilterHintCountIntent(body.Question) {
		if res, ok := resolveRoutedFilteredCount(r.Context(), h.llm, store, pa.pool, string(pa.scope), pa.identityID, body.Question, pa.history); ok {
			metrics.ObserveAIQueryRoute("count_routed")
			recordTurn(r, pa.tenant.ID, pa.convStore, pa.conv, body.Question, res.Answer)
			logSecurityEvent(r, "ai_query", "tenant_id", pa.tenant.ID)
			writeAskResult(w, res, pa.conv)
			return
		}
	}

	assistant := ai.NewAssistant(pa.pool, h.cpPool, h.queryEmbed, h.llm).WithMetrics(metrics.AI{})
	if h.reranker != nil {
		assistant = assistant.WithReranker(h.reranker, h.rerankCandidates)
	}
	res, err := assistant.Ask(r.Context(), ai.AskRequest{
		Question:     body.Question,
		Scope:        string(pa.scope),
		CallerUserID: pa.callerUserID,
		History:      pa.history,
	})
	if err != nil {
		slog.Error("ai ask failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", pa.tenant.ID, "err", err)
		fail(w, http.StatusBadGateway, "The assistant is temporarily unavailable.")
		return
	}

	metrics.ObserveAIQueryRoute("rag")
	recordTurn(r, pa.tenant.ID, pa.convStore, pa.conv, body.Question, res.Answer)
	logSecurityEvent(r, "ai_query", "tenant_id", pa.tenant.ID)
	writeAskResult(w, res, pa.conv)
}

// sseEvent is one SSE frame queued from the background dispatch goroutine to
// the AskStream handler goroutine, which is the only one allowed to write to
// the ResponseWriter (see channelSink).
type sseEvent struct {
	event string
	data  any
}

// channelSink adapts rag.StreamSink to a channel instead of writing directly:
// AskStream's dispatch (retrieval + generation, or a synchronous count) runs
// on a background goroutine so the handler's own goroutine can interleave
// heartbeats with real events, and http.ResponseWriter is not safe for
// concurrent writes from two goroutines — so exactly one goroutine (the
// handler's select loop) ever touches it. Every send respects ctx: once the
// handler gives up (client gone, absolute timeout), OnRetrieved/OnToken
// return ctx.Err() immediately instead of blocking forever against a channel
// nobody reads from anymore, which is what lets AskStream (and ChatStream
// beneath it) unwind promptly instead of leaking a goroutine.
type channelSink struct {
	ch  chan<- sseEvent
	ctx context.Context
}

func (s *channelSink) OnRetrieved(cites []ragcore.Citation) error {
	select {
	case s.ch <- sseEvent{event: "sources", data: map[string]any{"citations": cites}}:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *channelSink) OnToken(token string) error {
	select {
	case s.ch <- sseEvent{event: "token", data: token}:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// runAskDispatch is AskStream's counterpart to Ask's inline three-way
// dispatch (direct count / LLM-routed filtered count / RAG), reusing the
// exact same classification and count helpers so the two endpoints can never
// answer the same question differently. The two count paths compute their
// whole answer synchronously (no model streaming involved) and deliver it
// through sink as one "sources" event (empty citations) plus one "token"
// event carrying the full text — giving the client exactly one rendering
// path (accumulate "token" events, render "done") regardless of which route
// actually answered.
func runAskDispatch(ctx context.Context, h *AIOps, pa preparedAsk, store crmstore.Store, question string, sink *channelSink) (res ragcore.AskResult, route string, err error) {
	if keys, matched := classifyCountQuestion(question); matched {
		res, err = countCRMRecords(ctx, store, pa.pool, string(pa.scope), pa.identityID, keys)
		if err != nil {
			return res, "count_direct", err
		}
		if err := sink.OnRetrieved(res.Citations); err != nil {
			return res, "count_direct", err
		}
		if err := sink.OnToken(res.Answer); err != nil {
			return res, "count_direct", err
		}
		return res, "count_direct", nil
	}

	if hasFilterHintCountIntent(question) {
		if routed, ok := resolveRoutedFilteredCount(ctx, h.llm, store, pa.pool, string(pa.scope), pa.identityID, question, pa.history); ok {
			if err := sink.OnRetrieved(routed.Citations); err != nil {
				return routed, "count_routed", err
			}
			if err := sink.OnToken(routed.Answer); err != nil {
				return routed, "count_routed", err
			}
			return routed, "count_routed", nil
		}
	}

	assistant := ai.NewAssistant(pa.pool, h.cpPool, h.queryEmbed, h.llm).WithMetrics(metrics.AI{})
	if h.reranker != nil {
		assistant = assistant.WithReranker(h.reranker, h.rerankCandidates)
	}
	res, err = assistant.AskStream(ctx, ai.AskRequest{
		Question:     question,
		Scope:        string(pa.scope),
		CallerUserID: pa.callerUserID,
		History:      pa.history,
	}, sink)
	return res, "rag", err
}

// writeSSE marshals data as JSON and writes one SSE frame. data must marshal
// to a single line (json.Marshal never emits raw newlines), which is what
// makes the two-line "event:"/"data:" framing below valid per the SSE spec.
func writeSSE(w io.Writer, event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal sse data: %w", err)
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	return err
}

// streamMaxDuration is the absolute ceiling on one SSE stream regardless of
// how much progress it's making — the safety net against a wedged connection
// pinning a goroutine (and a maxConcurrentStreams slot) forever.
const streamMaxDuration = 5 * time.Minute

// streamWriteDeadline is re-armed (via http.ResponseController) on every SSE
// event, including heartbeats: how long the connection may go silent before
// it's considered dead. Not zero, or a wedged client with nothing further to
// send pins the handler goroutine forever. Requires statusRecorder.Unwrap
// (middleware/logging.go) to reach the real ResponseWriter through the
// global middleware chain.
const streamWriteDeadline = 30 * time.Second

// streamHeartbeatInterval is how often a ": ping" comment line is sent
// during any gap between real events — long enough to be cheap, short enough
// that neither streamWriteDeadline nor Cloudflare's own edge buffering ever
// sees true silence, including during retrieval before the first token.
const streamHeartbeatInterval = 15 * time.Second

// AskStream handles POST /api/tenant/ai/ask/stream — the SSE twin of Ask.
// Same chain (RequireAuth -> aiChain rate limits -> TenantResolver) and the
// identical security checks via prepareAsk; the difference is delivery: the
// answer streams token-by-token as text/event-stream instead of arriving as
// one JSON body once generation finishes.
//
// Event sequence: "sources" once (the retrieved set, before generation
// starts — render as "found N sources", never as citations: citedOnly
// filtering to what the answer actually references only happens in "done")
// -> "token"* as generated -> exactly one of "done" (the full result, same
// shape Ask's response carries) or "error". A ": ping" comment line may
// appear between any of these; per the SSE spec a client ignores lines that
// aren't a recognized field.
//
// The conversation turn is persisted only after "done": a client that
// disconnects mid-stream (or the absolute streamMaxDuration cap) leaves
// nothing in history, deliberately — a truncated assistant message would
// otherwise poison every later turn that replays it as context.
func (h *AIOps) AskStream(w http.ResponseWriter, r *http.Request) {
	body, pa, ok := h.prepareAsk(w, r)
	if !ok {
		return
	}

	if !h.acquireStream() {
		fail(w, http.StatusTooManyRequests, "The assistant is handling too many conversations right now — please try again in a moment.")
		return
	}
	defer h.releaseStream()

	// Everything above this line can still fail with an ordinary JSON 4xx/5xx
	// response. Nothing below it can: once the 200 + SSE headers are
	// written, the only way to signal failure to the client is an "error"
	// event, because the status line and headers are already on the wire.
	ctx, cancel := context.WithTimeout(r.Context(), streamMaxDuration)
	defer cancel()

	// http.ResponseController, not a w.(http.Flusher) type assertion, is the
	// only mechanism that reaches Flush/SetWriteDeadline through the global
	// RequestLogger(Recover(...)) chain: RequestLogger hands every handler a
	// *statusRecorder (middleware/logging.go) that embeds the
	// http.ResponseWriter interface — promoting only Header/Write/WriteHeader
	// — rather than implementing Flush itself, so a direct type assertion
	// against w always fails here even though the underlying connection
	// supports it. ResponseController instead walks the chain via each
	// wrapper's Unwrap() method, which statusRecorder provides.
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	armDeadline := func() { _ = rc.SetWriteDeadline(time.Now().Add(streamWriteDeadline)) }
	armDeadline()
	if err := rc.Flush(); err != nil {
		// Headers are already on the wire, so this can only be logged, not
		// turned into a 5xx: there is no client that still understands a
		// status-code response at this point.
		slog.Error("ai ask stream: initial flush unsupported", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", pa.tenant.ID, "err", err)
		return
	}

	store := crmstore.For(pa.tenant.DesignVersion)
	events := make(chan sseEvent, 4)
	sink := &channelSink{ch: events, ctx: ctx}

	type outcome struct {
		res   ragcore.AskResult
		route string
		err   error
	}
	// finalOutcome is written exactly once, by the background goroutine,
	// strictly before it closes events — never read until this handler's
	// select loop observes events closed. That ordering (not a second
	// channel) is what makes the plain variable safe to share across
	// goroutines: closing a channel happens-before a receive that returns
	// because it's closed (Go memory model), so the write below is always
	// visible by the time it's read. A separate buffered outcome channel
	// was tried first and dropped — with events also buffered, sends on
	// both can complete before this goroutine ever runs, so select's
	// pseudo-random choice among simultaneously-ready cases could pick the
	// outcome before events was ever drained, delivering "done" ahead of
	// buffered "token" events still sitting in the channel.
	var finalOutcome outcome
	go func() {
		res, route, err := runAskDispatch(ctx, h, pa, store, body.Question, sink)
		finalOutcome = outcome{res: res, route: route, err: err}
		close(events)
	}()

	heartbeat := time.NewTicker(streamHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case ev, open := <-events:
			if !open {
				out := finalOutcome
				if out.err != nil {
					slog.Error("ai ask stream failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", pa.tenant.ID, "route", out.route, "err", out.err)
					_ = writeSSE(w, "error", map[string]any{"success": false, "message": "The assistant is temporarily unavailable."})
					_ = rc.Flush()
					return
				}
				metrics.ObserveAIQueryRoute(out.route)
				recordTurn(r, pa.tenant.ID, pa.convStore, pa.conv, body.Question, out.res.Answer)
				logSecurityEvent(r, "ai_query", "tenant_id", pa.tenant.ID)
				data := map[string]any{"answer": out.res.Answer, "citations": out.res.Citations}
				if pa.conv != nil {
					data["conversation_id"] = pa.conv.ID
				}
				_ = writeSSE(w, "done", data)
				_ = rc.Flush()
				return
			}
			if err := writeSSE(w, ev.event, ev.data); err != nil {
				// The client is gone. Returning triggers the deferred
				// cancel(), which unwinds the background goroutine (its next
				// channelSink send, or ChatStream's own ctx check, sees
				// ctx.Done()) without this handler waiting on it.
				return
			}
			armDeadline()
			_ = rc.Flush()

		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			armDeadline()
			_ = rc.Flush()

		case <-ctx.Done():
			// Absolute cap hit, or the client disconnected (r.Context()
			// cancels on that — the disconnect case makes this write a
			// no-op in practice, which is fine). Either way nothing is
			// persisted: recordTurn is reachable only from the events-closed
			// success branch above.
			_ = writeSSE(w, "error", map[string]any{"success": false, "message": "The assistant took too long to respond."})
			_ = rc.Flush()
			return
		}
	}
}

// Reindex handles POST /api/tenant/ai/reindex (admin only). Enqueues every
// CRM record for re-embedding (used after an embedding-model change or backfill).
func (h *AIOps) Reindex(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "ai_reindex_denied", "tenant_id", tenant.ID)
		fail(w, http.StatusForbidden, "You do not have permission to reindex this workspace.")
		return
	}

	store := crmstore.For(tenant.DesignVersion)
	q := index.NewQueue(pool)
	enqueued := 0
	for _, key := range crmstore.CRMWorkflowKeys() {
		recs, err := store.ListRecords(r.Context(), pool, key, "all", "")
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to list records for reindex.")
			return
		}
		for _, rec := range recs {
			if err := q.Enqueue(r.Context(), rec.ID, "upsert"); err == nil {
				enqueued++
			}
		}
	}

	logSecurityEvent(r, "ai_reindex", "tenant_id", tenant.ID, "enqueued", enqueued)
	writeJSON(w, http.StatusAccepted, map[string]any{"success": true, "enqueued": enqueued})
}

// ReindexHelp handles POST /api/platform/ai/reindex-help. Platform-admin
// only. Re-embeds every docs/*.md file (compiled into the binary via
// stonesuite-backend/docs) into cp_rag_chunks — run after editing any file
// docs/ covers. Unlike Reindex (which enqueues CRM records for a background
// worker), this embeds synchronously in the request: the app-help corpus is
// small enough (today: one file) that a background queue would be pure
// overhead.
func (h *AIOps) ReindexHelp(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	isAdmin, err := h.cp.IsPlatformAdmin(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !isAdmin {
		logSecurityEvent(r, "ai_reindex_help_denied")
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return
	}

	store := ai.NewCPHelpStore(h.cpPool)
	res, err := ingest.IngestFS(r.Context(), h.docEmbed, store, docs.FS, ingest.DefaultChunkOpts)
	if err != nil {
		slog.Error("reindex help failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to reindex app-help docs.")
		return
	}

	logSecurityEvent(r, "ai_reindex_help", "ingested", len(res.Ingested), "failed", len(res.Failed))
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": res})
}
