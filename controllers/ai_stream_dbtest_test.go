//go:build dbtest

package controllers

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/ai"
	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

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
