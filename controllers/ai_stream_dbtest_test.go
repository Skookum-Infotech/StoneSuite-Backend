//go:build dbtest

package controllers

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Skookum-Infotech/go-rag/rag"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/ai"
	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// fakeEmbedder satisfies rag.Embedder with a fixed 768-dim vector (matching
// database/migrations/tenant/schema.sql's `vector(768)` columns) so the real
// "rag" dispatch route's retrieval step can run against the test DB without
// a live Ollama embedder — its actual values don't matter for these tests,
// since the seeded tenant/CP databases have nothing indexed to retrieve.
type fakeEmbedder struct{}

// fakeVector is what fakeEmbedder returns for every text. Non-zero on
// purpose: cosine distance to a zero vector is NaN, so nothing would ever
// clear the relevance floor. A chunk seeded with this same vector sits at
// distance 0 from every question.
func fakeVector() []float32 {
	v := make([]float32, 768)
	for i := range v {
		v[i] = 0.1
	}
	return v
}

func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = fakeVector()
	}
	return out, nil
}

// seedRAGChunk indexes one record chunk of recordType owned by ownerUserID,
// at distance 0 from every fakeEmbedder question, and returns its source id.
func seedRAGChunk(t *testing.T, pool *pgxpool.Pool, recordType, ownerUserID, content string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id))
	require.NoError(t, ai.NewRagStore(pool).Upsert(context.Background(), rag.Chunk{
		SourceID: id, OwnerUserID: ownerUserID, Kind: recordType,
		Content: content, ContentHash: id, Embedding: fakeVector(),
	}))
	return id
}

// testCPPool connects to the control-plane test database (TEST_CP_DATABASE_URL)
// directly — AIOps.cpPool needs a raw *pgxpool.Pool, and tenancy.ControlPlane
// (built by newSAMLTestControlPlane) doesn't expose the one it holds.
func testCPPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// sseFrame is one parsed "event: X\ndata: Y\n\n" block from a raw SSE body.
type sseFrame struct {
	event string
	data  string
}

// parseSSE splits a raw SSE response body into its frames, skipping ": ping"
// comment lines — a real client ignores comments the same way.
func parseSSE(body string) []sseFrame {
	var frames []sseFrame
	var cur sseFrame
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if cur.event != "" {
				frames = append(frames, cur)
			}
			cur = sseFrame{}
		}
	}
	return frames
}

// doAskStream runs h.AskStream through the tenancy resolver, matching how
// main.go's aiChain wires it in front of the real handler.
func doAskStream(resolver *tenancy.Resolver, h *AIOps, identityID, tenantID, requestBody string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/ai/ask/stream", strings.NewReader(requestBody))
	payload := middleware.UserContextPayload{ID: identityID, TenantID: tenantID}
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	resolver.Middleware(http.HandlerFunc(h.AskStream)).ServeHTTP(rec, req)
	return rec
}

// doAskStreamThroughGlobalChain is doAskStream but wrapped in
// middleware.RequestLogger, exactly as main.go's real global chain
// (RequestLogger(Recover(corsHandler)) -> ... -> aiChain -> AskStream) does.
// httptest.ResponseRecorder itself implements http.Flusher, so a test that
// calls resolver.Middleware(...).ServeHTTP(rec, req) directly (as
// doAskStream does) can pass even when the handler's http.Flusher type
// assertion would fail against the *statusRecorder RequestLogger actually
// hands it in production. Only this wrapped path proves the assertion
// survives the real chain.
func doAskStreamThroughGlobalChain(resolver *tenancy.Resolver, h *AIOps, identityID, tenantID, requestBody string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/ai/ask/stream", strings.NewReader(requestBody))
	payload := middleware.UserContextPayload{ID: identityID, TenantID: tenantID}
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	handler := middleware.RequestLogger(middleware.Recover(resolver.Middleware(http.HandlerFunc(h.AskStream))))
	handler.ServeHTTP(rec, req)
	return rec
}

// TestAIOpsAskStream_DirectCountRouteEndToEnd proves the SSE endpoint's
// wiring end-to-end through the cheapest of the three dispatch routes (no
// embedder or LLM call involved): headers, event ordering (sources -> token
// -> done), and the "done" event's shape matching what Ask's JSON body
// would have carried.
func TestAIOpsAskStream_DirectCountRouteEndToEnd(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	roleID := seedRBACTestRole(t, pool, "ai-stream-count", authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

	h := NewAIOps(nil, nil, nil, nil, nil) // direct count never touches cpPool/embedder/llm

	rec := doAskStream(resolver, h, identity.ID, tenant.ID, `{"question":"how many leads do we have?"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))

	frames := parseSSE(rec.Body.String())
	require.GreaterOrEqual(t, len(frames), 3, "want at least sources, token, done: %+v", frames)
	assert.Equal(t, "sources", frames[0].event)
	assert.Equal(t, "token", frames[1].event)
	last := frames[len(frames)-1]
	require.Equal(t, "done", last.event, "stream must end in exactly one done or error event")

	var done struct {
		Answer    string `json:"answer"`
		Citations []any  `json:"citations"`
	}
	require.NoError(t, json.Unmarshal([]byte(last.data), &done))
	assert.Contains(t, done.Answer, "0 leads")
}

// TestAIOpsAskStream_UnknownConversationIs404BeforeAnyStreaming proves a
// rejected conversation_id never reaches the point of committing to SSE
// headers — it must come back as an ordinary JSON 404, exactly like Ask,
// not an "error" SSE event.
func TestAIOpsAskStream_UnknownConversationIs404BeforeAnyStreaming(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	roleID := seedRBACTestRole(t, pool, "ai-stream-404", authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

	h := NewAIOps(nil, nil, nil, nil, nil)

	rec := doAskStream(resolver, h, identity.ID, tenant.ID, `{"question":"how many leads?","conversation_id":"00000000-0000-0000-0000-000000000000"}`)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, rec.Header().Get("Content-Type"), "event-stream")
}

// TestAIOpsAskStream_PersistsTurnOnlyOnDone proves the conversation-history
// side effect: a named conversation gets exactly one user + one assistant
// message appended, and its title set, only once the stream reaches "done".
func TestAIOpsAskStream_PersistsTurnOnlyOnDone(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	roleID := seedRBACTestRole(t, pool, "ai-stream-persist", authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

	convStore := ai.NewConversationStore(pool)
	conv, err := convStore.Create(context.Background(), user.ID)
	require.NoError(t, err)

	h := NewAIOps(nil, nil, nil, nil, nil)

	rec := doAskStream(resolver, h, identity.ID, tenant.ID,
		`{"question":"how many leads do we have?","conversation_id":"`+conv.ID+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	frames := parseSSE(rec.Body.String())
	last := frames[len(frames)-1]
	require.Equal(t, "done", last.event)
	var done struct {
		ConversationID string `json:"conversation_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(last.data), &done))
	assert.Equal(t, conv.ID, done.ConversationID)

	messages, err := convStore.Messages(context.Background(), conv.ID)
	require.NoError(t, err)
	require.Len(t, messages, 2, "want exactly one user + one assistant message recorded")
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "assistant", messages[1].Role)

	reloaded, found, err := convStore.Get(context.Background(), conv.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.NotEmpty(t, reloaded.Title, "title must be set from the first question")
}

// TestAIOpsAskStream_WorksThroughRealGlobalChain proves the streaming
// endpoint actually streams when reached through the same
// RequestLogger(Recover(...)) wrapping main.go puts in front of every route
// in production, not just through a bare httptest.ResponseRecorder. The
// handler asserts w.(http.Flusher); RequestLogger hands the handler a
// *statusRecorder, which embeds the http.ResponseWriter interface (promoting
// only Header/Write/WriteHeader) rather than implementing Flush itself. That
// assertion fails through this chain even though it succeeds against a bare
// httptest.ResponseRecorder (which does implement Flush directly) — which is
// exactly why every other test in this file, using doAskStream, missed it.
func TestAIOpsAskStream_WorksThroughRealGlobalChain(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	roleID := seedRBACTestRole(t, pool, "ai-stream-global-chain", authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

	h := NewAIOps(nil, nil, nil, nil, nil)

	rec := doAskStreamThroughGlobalChain(resolver, h, identity.ID, tenant.ID, `{"question":"how many leads do we have?"}`)

	require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	frames := parseSSE(rec.Body.String())
	require.GreaterOrEqual(t, len(frames), 3, "want at least sources, token, done: %+v", frames)
	last := frames[len(frames)-1]
	require.Equal(t, "done", last.event, "stream must end in exactly one done or error event")
}

// TestAIOpsAskStream_RealRagRouteEndToEnd proves the actual retrieval+LLM
// "rag" dispatch route — every other test in this file exercises only the
// cheap "count_direct" shortcut via NewAIOps(nil,nil,nil,nil,nil), which
// never calls h.queryEmbed or h.llm at all. A question that doesn't match
// classifyCountQuestion/hasFilterHintCountIntent forces the real path:
// Assistant.AskStream -> Orchestrator.AskStream -> rag.FakeStreamingLLM's
// ChatStream, streaming Tokens one at a time through the same channelSink
// and SSE framing as the count routes.
func TestAIOpsAskStream_RealRagRouteEndToEnd(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	cpPool := testCPPool(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	roleID := seedRBACTestRole(t, pool, "ai-stream-rag-route", authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

	seedRAGChunk(t, pool, "lead", user.ID, "Workflow: lead Name: Acme Status: Active")
	llm := &rag.FakeStreamingLLM{Tokens: []string{"Acme ", "is ", "active."}}
	h := NewAIOps(cpPool, fakeEmbedder{}, llm, nil, nil)

	// Deliberately not count-shaped (no "how many"/"count of") so
	// runAskDispatch falls through both count gates into the "rag" branch.
	rec := doAskStream(resolver, h, identity.ID, tenant.ID, `{"question":"what is going on with Acme?"}`)

	require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())
	frames := parseSSE(rec.Body.String())
	require.GreaterOrEqual(t, len(frames), 4, "want sources, 3 tokens, done: %+v", frames)
	assert.Equal(t, "sources", frames[0].event)

	var gotTokens []string
	for _, f := range frames[1 : len(frames)-1] {
		require.Equal(t, "token", f.event, "every frame between sources and done must be a token: %+v", f)
		var tok string
		require.NoError(t, json.Unmarshal([]byte(f.data), &tok))
		gotTokens = append(gotTokens, tok)
	}
	assert.Equal(t, []string{"Acme ", "is ", "active."}, gotTokens)

	last := frames[len(frames)-1]
	require.Equal(t, "done", last.event)
	var done struct {
		Answer string `json:"answer"`
	}
	require.NoError(t, json.Unmarshal([]byte(last.data), &done))
	assert.Equal(t, "Acme is active.", done.Answer)
}

// TestAIOpsAskStream_LLMFailureEmitsErrorEvent proves the "error" SSE event
// branch specifically (controllers/ai.go's events-closed/out.err != nil
// path) — every other test in this file only reaches "done". Forces it by
// giving the real rag route a FakeStreamingLLM configured to fail.
func TestAIOpsAskStream_LLMFailureEmitsErrorEvent(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	cpPool := testCPPool(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	roleID := seedRBACTestRole(t, pool, "ai-stream-llm-failure", authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

	seedRAGChunk(t, pool, "lead", user.ID, "Workflow: lead Name: Acme Status: Active")
	llm := &rag.FakeStreamingLLM{Err: assert.AnError}
	h := NewAIOps(cpPool, fakeEmbedder{}, llm, nil, nil)

	rec := doAskStream(resolver, h, identity.ID, tenant.ID, `{"question":"what is going on with Acme?"}`)

	require.Equal(t, http.StatusOK, rec.Code, "the error surfaces as an SSE event, not an HTTP status")
	frames := parseSSE(rec.Body.String())
	last := frames[len(frames)-1]
	require.Equal(t, "error", last.event, "an LLM failure must end the stream in exactly one error event: %+v", frames)

	var errPayload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(last.data), &errPayload))
	assert.False(t, errPayload.Success)
	assert.NotEmpty(t, errPayload.Message)
}

// TestAIOpsAskStream_CtxDoneEmitsErrorEvent proves the streamMaxDuration
// absolute-cap branch (controllers/ai.go's ctx.Done() case) — the third of
// the three ways a stream can end, and the one no other test in this file
// reaches. Uses a StreamingLLMClient that blocks past the deadline instead
// of the real 5-minute streamMaxDuration, which nothing here can wait out.
func TestAIOpsAskStream_CtxDoneEmitsErrorEvent(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	cpPool := testCPPool(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	roleID := seedRBACTestRole(t, pool, "ai-stream-ctx-done", authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

	// Blocks on ctx.Done() itself rather than a fixed sleep, so this test
	// finishes as soon as the (short, test-supplied) context is cancelled
	// instead of waiting out any fixed duration.
	seedRAGChunk(t, pool, "lead", user.ID, "Workflow: lead Name: Acme Status: Active")
	llm := &blockingStreamingLLM{}
	h := NewAIOps(cpPool, fakeEmbedder{}, llm, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/tenant/ai/ask/stream", strings.NewReader(`{"question":"what is going on with Acme?"}`))
	payload := middleware.UserContextPayload{ID: identity.ID, TenantID: tenant.ID}
	ctx, cancel := context.WithTimeout(req.Context(), 50*time.Millisecond)
	defer cancel()
	req = req.WithContext(context.WithValue(ctx, middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	resolver.Middleware(http.HandlerFunc(h.AskStream)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	frames := parseSSE(rec.Body.String())
	require.NotEmpty(t, frames)
	last := frames[len(frames)-1]
	require.Equal(t, "error", last.event, "a cancelled context must end the stream in exactly one error event: %+v", frames)

	var errPayload struct {
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(last.data), &errPayload))
	assert.Equal(t, "The assistant took too long to respond.", errPayload.Message)
}

// blockingStreamingLLM satisfies both rag.LLMClient and rag.StreamingLLMClient,
// blocking on ctx.Done() rather than ever completing — used to force
// AskStream's ctx.Done() branch deterministically instead of waiting out the
// real 5-minute streamMaxDuration.
type blockingStreamingLLM struct{}

func (blockingStreamingLLM) Chat(ctx context.Context, _ string, _ []rag.Message) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (blockingStreamingLLM) ChatStream(ctx context.Context, _ string, _ []rag.Message, _ func(string) error) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
