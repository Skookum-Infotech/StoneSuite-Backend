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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/aisettings"
	"stonesuite-backend/authz"
	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// docExtractDBEnv is one servable tenant with two callers (A and B), each able
// to create and read their own sales orders.
type docExtractDBEnv struct {
	pool                 *pgxpool.Pool
	resolver             *tenancy.Resolver
	tenantID             string
	identityA, identityB *tenancy.Identity
	userA, userB         *userstore.User
	h                    *DocExtractOps
}

func newDocExtractDBEnv(t *testing.T) *docExtractDBEnv {
	t.Helper()
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	e := &docExtractDBEnv{pool: pool, resolver: tenancy.NewResolver(cp, tenancy.NewRouter(nil)), tenantID: tenant.ID}
	e.identityA, e.userA = seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	e.identityB, e.userB = seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	for _, u := range []*userstore.User{e.userA, e.userB} {
		for _, act := range []authz.Action{authz.ActionCreate, authz.ActionRead} {
			role := seedRBACTestRole(t, pool, "docextract", authz.Grant{Resource: authz.ResourceSalesOrder, Action: act, Scope: authz.ScopeOwn})
			require.NoError(t, authz.AssignRole(context.Background(), pool, u.ID, role))
		}
	}
	e.h = NewDocExtractOps(nil, &fakeDocQueue{}, testCPPool(t), aisettings.NewCache(nil), DocExtractSettings{
		Enabled: true, MaxBytes: 10 << 20, DailyCap: 100, StagingMaxBytes: 1 << 30, StagingTTL: 24 * time.Hour, PresignTTL: time.Minute,
	})
	return e
}

func (e *docExtractDBEnv) serve(handler http.HandlerFunc, identityID, method, target, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if id != "" {
		req.SetPathValue("id", id)
	}
	payload := middleware.UserContextPayload{ID: identityID, TenantID: e.tenantID}
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	e.resolver.Middleware(handler).ServeHTTP(rec, req)
	return rec
}

// readyExtraction inserts a ready extraction owned by identityID with result.
func (e *docExtractDBEnv) readyExtraction(t *testing.T, identityID string, result docextractjob.ResultDoc) *docextractjob.Extraction {
	t.Helper()
	ctx := context.Background()
	s := docextractjob.NewStore(e.pool)
	ex, err := s.Create(ctx, docextractjob.CreateParams{DocType: string(docextract.DocTypeSalesOrder), OwnerIdentityID: identityID,
		FileName: "PO-4471.pdf", ContentType: "application/pdf", SizeBytes: 1000, TenantSlug: "acme", Extension: "pdf", TTL: 24 * time.Hour})
	require.NoError(t, err)
	_, _, err = s.MarkQueued(ctx, ex.ID, identityID, testSHA, "job")
	require.NoError(t, err)
	require.NoError(t, s.MarkRunning(ctx, ex.ID))
	// A per-extraction sha: a shared one makes every earlier used extraction a
	// "same file" duplicate of this one once GET re-matches it.
	require.NoError(t, s.SaveResult(ctx, ex.ID, docextractjob.SaveResultParams{SHA256: "secretsha" + uniqueSuffix(), Method: docextract.MethodParser, Result: result}))
	return ex
}

// uniqueSuffix is a per-call suffix for names that must not collide.
func uniqueSuffix() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }

// seedEmployee inserts the employee row userID's records are owned through.
func (e *docExtractDBEnv) seedEmployee(t *testing.T, userID string) int {
	t.Helper()
	var empID int
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1, 'Doc', 'Extract', $2, 1) RETURNING employee_id`, userID, "docx-"+uniqueSuffix()+"@example.test").Scan(&empID))
	return empID
}

// seedActiveCustomer inserts an active customer named name.
func (e *docExtractDBEnv) seedActiveCustomer(t *testing.T, name string) int {
	t.Helper()
	var custID int
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		INSERT INTO customer (record_type, customer_name, customer_crm_status, customer_created_by)
		SELECT rt.record_type_id, $1, cs.crm_status_id, 1
		FROM lkp_record_type rt, lkp_crm_status cs
		WHERE rt.record_type_code = 'CUST' AND cs.crm_status_code = 'CACT' LIMIT 1
		RETURNING customer_id`, name).Scan(&custID))
	return custID
}

// seedSO inserts a live (Open) sales order for custID owned by empID.
func (e *docExtractDBEnv) seedSO(t *testing.T, custID, empID int, poNumber string) string {
	t.Helper()
	suffix := uniqueSuffix()
	var u string
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id, sales_order_number,
		                         sales_order_po_number, sales_order_subtotal, sales_order_grand_total,
		                         sales_order_created_by, sales_order_owner_id)
		SELECT rt.record_type_id, rs.record_status_id, $1, $2::text, $3::text, 10, 10, $4, $4
		FROM lkp_record_type rt
		JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id AND rs.record_status_code = 'OPEN'
		WHERE rt.record_type_code = 'SORD'
		RETURNING sales_order_uuid::text`, custID, "SO"+suffix[len(suffix)-12:], poNumber, empID).Scan(&u))
	return u
}

// seedOwnedSO inserts a live sales order, for its own new customer, owned by
// userID's employee.
func (e *docExtractDBEnv) seedOwnedSO(t *testing.T, userID string) string {
	t.Helper()
	suffix := uniqueSuffix()
	return e.seedSO(t, e.seedActiveCustomer(t, "DocX"+suffix), e.seedEmployee(t, userID), "PO-"+suffix)
}

func TestDocExtract_DB_GetIsOwnerOnly(t *testing.T) {
	e := newDocExtractDBEnv(t)
	ex := e.readyExtraction(t, e.identityA.ID, docextractjob.ResultDoc{})

	rec := e.serve(e.h.Get, e.identityB.ID, http.MethodGet, "/x", ex.ID, "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "another owner's extraction id is a 404, never 403")
	assert.NotContains(t, rec.Body.String(), "PO-4471.pdf")

	rec = e.serve(e.h.Get, e.identityA.ID, http.MethodGet, "/x", ex.ID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, secret := range []string{"_staging", "secretsha", e.identityA.ID} {
		assert.NotContains(t, rec.Body.String(), secret)
	}

	// B can neither mutate A's extraction.
	for name, h := range map[string]http.HandlerFunc{"notify": e.h.Notify, "discard": e.h.Discard, "presign": e.h.Presign} {
		rec = e.serve(h, e.identityB.ID, http.MethodPost, "/x", ex.ID, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, name)
	}
	got, err := docextractjob.NewStore(e.pool).GetInternal(context.Background(), ex.ID)
	require.NoError(t, err)
	assert.Equal(t, docextractjob.StatusReady, got.Status)
}

func TestDocExtract_DB_DuplicateLinksAreScopeFiltered(t *testing.T) {
	e := newDocExtractDBEnv(t)
	// Real duplicates, not hand-written ones: GET re-matches a ready
	// extraction against live data, so both orders must genuinely share the
	// document's customer and PO number. One belongs to A, one to B.
	name, po := "DupCo"+uniqueSuffix(), "PO-"+uniqueSuffix()
	cust := e.seedActiveCustomer(t, name)
	mine := e.seedSO(t, cust, e.seedEmployee(t, e.userA.ID), po)
	theirs := e.seedSO(t, cust, e.seedEmployee(t, e.userB.ID), po)
	ex := e.readyExtraction(t, e.identityA.ID, docextractjob.ResultDoc{Extracted: docextract.Result{
		Header: docextract.Header{CustomerName: docextract.Field{Value: name}, PONumber: docextract.Field{Value: po}},
	}})

	rec := e.serve(e.h.Get, e.identityA.ID, http.MethodGet, "/x", ex.ID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Extraction docExtractView `json:"extraction"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	dups := resp.Extraction.Result.Duplicates
	require.Len(t, dups, 2, "both orders with the document's customer and PO are duplicates")
	var linked, hidden []docextractjob.Duplicate
	for _, d := range dups {
		if d.RecordUUID == "" {
			hidden = append(hidden, d)
		} else {
			linked = append(linked, d)
		}
	}
	require.Len(t, linked, 1)
	assert.Equal(t, mine, linked[0].RecordUUID, "the caller's own order is linked")
	require.Len(t, hidden, 1, "another owner's order is reported but not linked")
	assert.Empty(t, hidden[0].Number)
	assert.NotEmpty(t, hidden[0].Reason)
	assert.NotContains(t, rec.Body.String(), theirs)
	assert.NotContains(t, rec.Body.String(), e.userB.ID, "the other owner's user id is never exposed")
}

func TestDocExtract_DB_Complete(t *testing.T) {
	e := newDocExtractDBEnv(t)
	mine := e.seedOwnedSO(t, e.userA.ID)
	theirs := e.seedOwnedSO(t, e.userB.ID)
	ex := e.readyExtraction(t, e.identityA.ID, docextractjob.ResultDoc{})
	body := func(uuid string) string { return `{"recordUuid":"` + uuid + `","saved":{}}` }

	rec := e.serve(e.h.Complete, e.identityA.ID, http.MethodPost, "/x", ex.ID, body(theirs))
	assert.Equal(t, http.StatusNotFound, rec.Code, "a record the caller did not create is a 404")
	rec = e.serve(e.h.Complete, e.identityA.ID, http.MethodPost, "/x", ex.ID, body("not-a-uuid"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = e.serve(e.h.Complete, e.identityB.ID, http.MethodPost, "/x", ex.ID, body(theirs))
	assert.Equal(t, http.StatusNotFound, rec.Code, "B cannot complete A's extraction")
	got, err := docextractjob.NewStore(e.pool).GetInternal(context.Background(), ex.ID)
	require.NoError(t, err)
	require.Equal(t, docextractjob.StatusReady, got.Status, "refused attempts leave the extraction ready")

	rec = e.serve(e.h.Complete, e.identityA.ID, http.MethodPost, "/x", ex.ID, body(mine))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = e.serve(e.h.Complete, e.identityA.ID, http.MethodPost, "/x", ex.ID, body(mine))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"already_used"`)

	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_logs WHERE action = $1 AND resource = 'sales_order' AND resource_id = $2`,
		auditActionCreatedFromDocument, mine).Scan(&n))
	assert.Equal(t, 1, n, "creation provenance is audited on the order's own trail")
}

func TestDocExtract_DB_PermissionDeniedIs403(t *testing.T) {
	e := newDocExtractDBEnv(t)
	cp := newSAMLTestControlPlane(t)
	identityC, _ := seedRBACTestIdentity(t, cp, e.pool, e.tenantID, "pw") // no roles
	rec := e.serve(e.h.Create, identityC.ID, http.MethodPost, "/x", "", createBody)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

func TestAIStatus_DocumentExtractionGate(t *testing.T) {
	e := newAITestEnv(t)
	status := func(h *AIOps) bool {
		req := httptest.NewRequest(http.MethodGet, "/api/tenant/ai/status", nil)
		payload := middleware.UserContextPayload{ID: e.identity.ID, TenantID: e.tenantID}
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
		rec := httptest.NewRecorder()
		e.resolver.Middleware(http.HandlerFunc(h.Status)).ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp struct {
			Status struct {
				Available          bool `json:"available"`
				DocumentExtraction bool `json:"documentExtraction"`
			} `json:"status"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return resp.Status.DocumentExtraction
	}
	assert.False(t, status(e.h), "flag off")
	assert.True(t, status(e.h.WithDocExtractEnabled(true)), "flag on and AI available")
}
