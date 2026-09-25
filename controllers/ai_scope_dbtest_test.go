//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"stonesuite-backend/userstore"
)

// aiTestEnv is one servable tenant plus a caller, ready to ask.
type aiTestEnv struct {
	pool     *pgxpool.Pool
	resolver *tenancy.Resolver
	tenantID string
	identity *tenancy.Identity
	user     *userstore.User
	h        *AIOps
}

// newAITestEnv seeds a tenant and a caller holding grants (none = help-only),
// with a streaming fake LLM that answers citing source [1].
func newAITestEnv(t *testing.T, grants ...authz.Grant) *aiTestEnv {
	t.Helper()
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	for _, g := range grants {
		roleID := seedRBACTestRole(t, pool, "ai-scope", g)
		require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))
	}
	llm := &rag.FakeStreamingLLM{Reply: "See [1].", Tokens: []string{"See ", "[1]."}}
	return &aiTestEnv{
		pool: pool, resolver: tenancy.NewResolver(cp, tenancy.NewRouter(nil)),
		tenantID: tenant.ID, identity: identity, user: user,
		h: NewAIOps(testCPPool(t), fakeEmbedder{}, llm, nil, nil),
	}
}

// uniqueToken is a single search term no other test's chunk contains.
func uniqueToken() string { return fmt.Sprintf("zq%d", time.Now().UnixNano()) }

func (e *aiTestEnv) ask(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/ai/ask", strings.NewReader(body))
	payload := middleware.UserContextPayload{ID: e.identity.ID, TenantID: e.tenantID}
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	e.resolver.Middleware(http.HandlerFunc(e.h.Ask)).ServeHTTP(rec, req)
	return rec
}

type sourcesPayload struct {
	Citations []struct {
		SourceType string `json:"source_type"`
		SourceID   string `json:"source_id"`
		RecordType string `json:"record_type"`
	} `json:"citations"`
}

// streamSources runs a stream ask and returns its "sources" event.
func (e *aiTestEnv) streamSources(t *testing.T, question string) sourcesPayload {
	t.Helper()
	rec := doAskStream(e.resolver, e.h, e.identity.ID, e.tenantID, `{"question":"`+question+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	frames := parseSSE(rec.Body.String())
	require.NotEmpty(t, frames)
	require.Equal(t, "sources", frames[0].event, "frames: %+v", frames)
	var src sourcesPayload
	require.NoError(t, json.Unmarshal([]byte(frames[0].data), &src))
	return src
}

// TestAIAsk_UngrantedTypeNeverRetrieved is the regression test for the
// cross-type leak: a caller with lead:read "all" and NO customer grant used
// to be scoped "all" across every rag_chunks row and retrieved customers.
func TestAIAsk_UngrantedTypeNeverRetrieved(t *testing.T) {
	e := newAITestEnv(t, authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	// A keyword only these two chunks contain: the lexical arm is then
	// guaranteed to surface the customer chunk if scope lets it through, no
	// matter how many other test chunks tie on vector distance in the shared
	// test database. Without it this test could pass against the old leak.
	tok := uniqueToken()
	customerID := seedRAGChunk(t, e.pool, "customer", e.user.ID, "Workflow: customer Name: Secret Customer "+tok)
	leadID := seedRAGChunk(t, e.pool, "lead", e.user.ID, "Workflow: lead Name: Visible Lead "+tok)

	src := e.streamSources(t, "tell me about "+tok)
	var sawLead bool
	for _, c := range src.Citations {
		sawLead = sawLead || c.SourceID == leadID
		assert.NotEqual(t, customerID, c.SourceID, "a customer record reached a caller with no customer grant")
		if c.SourceType == ai.CorpusRecords {
			assert.Equal(t, "lead", c.RecordType, "every record citation carries its type, and only granted types appear")
		}
	}
	assert.True(t, sawLead, "the granted lead must still be retrievable: %+v", src.Citations)

	rec := e.ask(t, `{"question":"how many customers do we have?"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "You don't have access to customer records.")
	assert.NotRegexp(t, `You have \d+ customer`, rec.Body.String(), "an ungranted type must not be counted")
}

// TestAIAsk_NoGrantsIsHelpOnlyNot403: a user with no CRM read grant can still
// ask how to use the product; record chunks never reach them.
func TestAIAsk_NoGrantsIsHelpOnlyNot403(t *testing.T) {
	e := newAITestEnv(t)
	tok := uniqueToken()
	recordID := seedRAGChunk(t, e.pool, "lead", e.user.ID, "Workflow: lead Name: Should Not Appear "+tok)

	src := e.streamSources(t, "tell me about "+tok)
	for _, c := range src.Citations {
		assert.NotEqual(t, recordID, c.SourceID)
		assert.NotEqual(t, ai.CorpusRecords, c.SourceType, "help-only mode must never search records")
	}
}

func TestAIAsk_NonStreamingReturnsTypedCitationsAndPersistsTurn(t *testing.T) {
	e := newAITestEnv(t, authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeOwn})
	seedRAGChunk(t, e.pool, "lead", e.user.ID, "Workflow: lead Name: Acme")
	conv, err := ai.NewConversationStore(e.pool).Create(context.Background(), e.user.ID)
	require.NoError(t, err)

	rec := e.ask(t, `{"question":"what is Acme?","conversation_id":"`+conv.ID+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Answer    string `json:"answer"`
			Truncated bool   `json:"truncated"`
			Citations []struct {
				RecordType string `json:"record_type"`
			} `json:"citations"`
		} `json:"data"`
		ConversationID string `json:"conversation_id"`
		Persisted      bool   `json:"persisted"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, conv.ID, resp.ConversationID)
	assert.True(t, resp.Persisted)
	require.Len(t, resp.Data.Citations, 1)
	assert.Equal(t, "lead", resp.Data.Citations[0].RecordType)

	msgs, err := ai.NewConversationStore(e.pool).Messages(context.Background(), conv.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 2, "one turn = question + answer, saved together")
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "assistant", msgs[1].Role)
}

func TestAIAsk_OtherUsersConversationIs404(t *testing.T) {
	e := newAITestEnv(t, authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	someoneElse, err := ai.NewConversationStore(e.pool).Create(context.Background(), "aaaaaaaa-0000-0000-0000-00000000dead")
	require.NoError(t, err)

	rec := e.ask(t, `{"question":"what is Acme?","conversation_id":"`+someoneElse.ID+`"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, "not 403: another user's conversation must be indistinguishable from none")
}

// TestAIAsk_BusyIs429WithCodeOnBothEndpoints: with the tenant's model slot
// taken, both endpoints refuse with the distinguishable busy 429 — the
// non-streaming endpoint no longer bypasses the cap.
func TestAIAsk_BusyIs429WithCodeOnBothEndpoints(t *testing.T) {
	e := newAITestEnv(t, authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	e.h.slotWait = 50 * time.Millisecond
	release, ok := e.h.slots.acquire(context.Background(), e.tenantID)
	require.True(t, ok)
	defer release()

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"ask":    e.ask(t, `{"question":"what is Acme?"}`),
		"stream": doAskStream(e.resolver, e.h, e.identity.ID, e.tenantID, `{"question":"what is Acme?"}`),
	} {
		assert.Equal(t, http.StatusTooManyRequests, rec.Code, name)
		assert.Equal(t, "5", rec.Header().Get("Retry-After"), name)
		assert.Contains(t, rec.Body.String(), `"code":"assistant_busy"`, name)
	}

	// A zero-LLM direct count needs no slot, so it still answers.
	rec := e.ask(t, `{"question":"how many leads do we have?"}`)
	assert.Equal(t, http.StatusOK, rec.Code, "count_direct must not wait on a model slot")
}
