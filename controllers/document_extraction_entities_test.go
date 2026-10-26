package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
	"stonesuite-backend/proposedaction"
	"stonesuite-backend/proposedentity"
)

const (
	entExtractionID = "11111111-2222-4333-8444-555555555555"
	entKeyCustomer  = "pe_aaaaaaaaaaaa"
	entKeyItem      = "pe_bbbbbbbbbbbb"
)

// fakeDocActionStore is an in-memory docExtractActionStore.
type fakeDocActionStore struct {
	rows     []proposedaction.Action // newest first
	active   *proposedaction.Action
	listErr  error
	proposed []proposedaction.Proposal
}

func (f *fakeDocActionStore) ListBySourceRef(context.Context, string, proposedaction.Source, string) ([]proposedaction.Action, error) {
	return f.rows, f.listErr
}

func (f *fakeDocActionStore) FindActiveBySourceRef(context.Context, string, proposedaction.Source, string) (proposedaction.Action, bool, error) {
	if f.active == nil {
		return proposedaction.Action{}, false, nil
	}
	return *f.active, true, nil
}

func (f *fakeDocActionStore) Propose(_ context.Context, _ proposedaction.Querier, p proposedaction.Proposal) (string, error) {
	f.proposed = append(f.proposed, p)
	return "act-new", nil
}

func (f *fakeDocActionStore) Get(_ context.Context, _, id string) (proposedaction.Action, error) {
	return proposedaction.Action{ID: id, Kind: proposedaction.KindCreate, Status: proposedaction.StatusPending}, nil
}

// entEnv wires a DocExtractOps for the propose-entity tests.
type entEnv struct {
	*docExtractEnv
	actions *fakeDocActionStore
	denied  map[authz.Resource]bool
	checked []authz.Resource
	logs    *bytes.Buffer
}

func newEntEnv(t *testing.T) *entEnv {
	t.Helper()
	base := newDocExtractEnv(t)
	e := &entEnv{docExtractEnv: base, actions: &fakeDocActionStore{}, denied: map[authz.Resource]bool{}, logs: &bytes.Buffer{}}
	e.h.cfg.ProposeEntities = true
	e.h.newActionStore = func(*pgxpool.Pool) docExtractActionStore { return e.actions }
	e.h.check = func(_ context.Context, _ *docExtractCaller, res authz.Resource, _ authz.Action) (authz.Decision, error) {
		e.checked = append(e.checked, res)
		return authz.Decision{Allowed: !e.denied[res], Scope: authz.ScopeOwn}, nil
	}
	e.h.refresh = func(_ context.Context, _ *docExtractCaller, _ *docextractjob.Extraction, s docextractjob.ResultDoc) (docextractjob.ResultDoc, error) {
		return s, nil
	}
	e.h.callerUserID = func(context.Context, *docExtractCaller) (string, error) { return "user-1", nil }
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(e.logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return e
}

// seedReady stores a ready sales order extraction with a customer and an item proposal.
func (e *entEnv) seedReady(t *testing.T) *docextractjob.Extraction {
	t.Helper()
	ex := seedExtraction(e.docExtractEnv, docextractjob.StatusReady)
	doc := docextractjob.ResultDoc{ProposedEntities: []docextractjob.ProposedEntity{
		{Key: entKeyCustomer, Kind: docextractjob.ProposedKindCustomer, Name: "Acme Stone", DocType: "sales_order",
			Needs: []string{docextractjob.NeedContactEmail}},
		{Key: entKeyItem, Kind: docextractjob.ProposedKindItem, Name: "Slab", DocType: "sales_order",
			Fields: docextractjob.ProposedFields{SKU: "SLAB-1"}},
	}}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	ex.Result = raw
	return ex
}

func (e *entEnv) propose(key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	req.SetPathValue("id", entExtractionID)
	req.SetPathValue("key", key)
	rec := httptest.NewRecorder()
	e.h.ProposeEntity(rec, req)
	return rec
}

func TestProposeEntity_FlagOffIs404(t *testing.T) {
	e := newEntEnv(t)
	e.seedReady(t)
	e.h.cfg.ProposeEntities = false
	rec := e.propose(entKeyCustomer, `{"overrides":{"contactEmail":"a@b.example"}}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "Not found.")
	assert.Empty(t, e.actions.proposed)
}

func TestProposeEntity_Gates(t *testing.T) {
	const okBody = `{"overrides":{"contactEmail":"buyer@acme.example"}}`
	tests := []struct {
		name     string
		key      string
		body     string
		mutate   func(e *entEnv, ex *docextractjob.Extraction)
		wantCode int
		wantBody string
	}{
		{"other owner", entKeyCustomer, okBody, func(_ *entEnv, ex *docextractjob.Extraction) { ex.OwnerIdentityID = "someone-else" }, 404, "Document extraction not found"},
		{"unknown key", "pe_nope", okBody, nil, 404, "Proposed record not found."},
		{"not ready", entKeyCustomer, okBody, func(_ *entEnv, ex *docextractjob.Extraction) { ex.Status = docextractjob.StatusQueued }, 409, `"code":"invalid_state"`},
		{"already used", entKeyCustomer, okBody, func(_ *entEnv, ex *docextractjob.Extraction) { ex.Status = docextractjob.StatusUsed }, 409, `"code":"already_used"`},
		{"unknown override field", entKeyCustomer, `{"overrides":{"contactEmail":"a@b.example","name":"Evil"}}`, nil, 400, "Invalid request body"},
		{"unknown top-level field", entKeyCustomer, `{"payload":{}}`, nil, 400, "Invalid request body"},
		{"missing needs email", entKeyCustomer, ``, nil, 422, "contactEmail is required"},
		{"blank needs email", entKeyCustomer, `{"overrides":{"contactEmail":"  "}}`, nil, 422, "contactEmail is required"},
		{"invalid email", entKeyCustomer, `{"overrides":{"contactEmail":"nope"}}`, nil, 422, "email"},
		{"target permission denied", entKeyCustomer, okBody, func(e *entEnv, _ *docextractjob.Extraction) { e.denied[authz.ResourceCustomer] = true }, 403, "permission"},
		{"unconfigured store", entKeyCustomer, okBody, func(e *entEnv, _ *docextractjob.Extraction) { e.h.newActionStore = nil }, 503, "not available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEntEnv(t)
			ex := e.seedReady(t)
			if tt.mutate != nil {
				tt.mutate(e, ex)
			}
			rec := e.propose(tt.key, tt.body)
			assert.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tt.wantBody)
			assert.Empty(t, e.actions.proposed, "a refused request must not create a proposal")
			assert.Contains(t, rec.Body.String(), `"success":false`)
		})
	}
}

func TestProposeEntity_PermissionDeniedIsLogged(t *testing.T) {
	e := newEntEnv(t)
	e.seedReady(t)
	e.denied[authz.ResourceCustomer] = true
	rec := e.propose(entKeyCustomer, `{"overrides":{"contactEmail":"buyer@acme.example"}}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	out := e.logs.String()
	assert.Contains(t, out, `"security_event":"permission_denied"`)
	assert.Contains(t, out, `"resource":"customer"`)
	assert.Contains(t, out, docExtractViaForTest)
	assert.NotContains(t, out, "buyer@acme.example")
}

// docExtractViaForTest pins the via label of the denial event.
const docExtractViaForTest = `"via":"docextract_propose"`

func TestProposeEntity_OtherOwnerIsLogged(t *testing.T) {
	e := newEntEnv(t)
	e.seedReady(t).OwnerIdentityID = "someone-else"
	rec := e.propose(entKeyCustomer, "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, e.logs.String(), `"security_event":"doc_extract_idor"`)
}

func TestProposeEntity_CreatesFromStoredEntityOnly(t *testing.T) {
	e := newEntEnv(t)
	e.seedReady(t)
	rec := e.propose(entKeyCustomer, `{"overrides":{"contactEmail":"buyer@acme.example"}}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"success":true`)
	assert.Contains(t, rec.Body.String(), `"id":"act-new"`)
	require.Len(t, e.actions.proposed, 1)
	p := e.actions.proposed[0]
	assert.Equal(t, proposedaction.KindCreate, p.Kind)
	assert.Equal(t, proposedaction.SourceDocExtract, p.Source)
	assert.Equal(t, string(authz.ResourceCustomer), p.Resource)
	assert.Equal(t, proposedaction.Caller{IdentityID: testIdentity, UserID: "user-1"}, p.Caller)
	var got proposedentity.CustomerPayload
	require.NoError(t, json.Unmarshal(p.Payload, &got))
	assert.Equal(t, "Acme Stone", got.Name, "name comes from the stored entity")
	assert.Equal(t, "buyer@acme.example", got.ContactEmail)
	assert.Equal(t, entExtractionID+":"+entKeyCustomer, got.SourceRef)
}

func TestProposeEntity_ItemSKUOverrideOnlyWhenNeeded(t *testing.T) {
	e := newEntEnv(t)
	ex := e.seedReady(t)
	var captured proposedentity.ProposeInput
	e.h.buildPayload = func(_ context.Context, _ *docExtractCaller, in proposedentity.ProposeInput) (authz.Resource, json.RawMessage, error) {
		captured = in
		return authz.ResourceInventoryItem, json.RawMessage(`{}`), nil
	}
	rec := e.propose(entKeyItem, `{"overrides":{"sku":"HACK-1"}}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "SLAB-1", captured.SKU, "the document sku wins when the entity does not need one")
	assert.Equal(t, proposedentity.SideSales, captured.Side)

	var doc docextractjob.ResultDoc
	require.NoError(t, json.Unmarshal(ex.Result, &doc))
	doc.ProposedEntities[1].Fields.SKU = ""
	doc.ProposedEntities[1].Needs = []string{docextractjob.NeedSKU}
	ex.Result, _ = json.Marshal(doc)
	assert.Equal(t, http.StatusUnprocessableEntity, e.propose(entKeyItem, "").Code)
	require.Equal(t, http.StatusCreated, e.propose(entKeyItem, `{"overrides":{"sku":"NEW-9"}}`).Code)
	assert.Equal(t, "NEW-9", captured.SKU)
}

func TestProposeEntity_IdempotentReturnsExisting(t *testing.T) {
	e := newEntEnv(t)
	e.seedReady(t)
	e.actions.active = &proposedaction.Action{ID: "act-existing", Status: proposedaction.StatusPending}
	rec := e.propose(entKeyCustomer, `{"overrides":{"contactEmail":"buyer@acme.example"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"id":"act-existing"`)
	assert.Empty(t, e.actions.proposed)
}

func TestProposeEntity_PurchaseSide(t *testing.T) {
	in, missing := proposeInputFor(docextractjob.ProposedEntity{Kind: docextractjob.ProposedKindItem, Name: "x", Fields: docextractjob.ProposedFields{SKU: "S"}},
		proposeOverrides{}, "ref", string(docextract.DocTypePurchaseOrder))
	assert.Empty(t, missing)
	assert.Equal(t, proposedentity.SidePurchase, in.Side)
}
