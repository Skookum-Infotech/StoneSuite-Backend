// controllers/salesorder_drawing_dbtest_test.go
//go:build dbtest

package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/tenancy"
)

const validAreaJSON = `{"id":"a1","name":"Area 1","code":"","sortOrder":0,"pieces":[{"id":"p1","label":"A-1","shape":"rect","outline":[{"x":0,"y":0},{"x":600,"y":0},{"x":600,"y":-400},{"x":0,"y":-400}],"position":{"x":0,"y":0},"rotationDeg":0,"thicknessMm":30,"edges":[],"seams":[],"cutouts":[]}],"cabinets":[],"notes":[]}`

// drawingTestEnv wires a real mux + tenancy resolver for the drawing routes
// and seeds one sales order the caller has the given grants on.
type drawingTestEnv struct {
	t          *testing.T
	mux        http.Handler
	identityID string
	tenantID   string
	soUUID     string
}

func newDrawingTestEnv(t *testing.T, grants ...authz.Grant) *drawingTestEnv {
	t.Helper()
	withTestJWTSecret(t)
	ctx := context.Background()
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	for _, g := range grants {
		roleID := seedRBACTestRole(t, pool, "drawing-test", g)
		require.NoError(t, authz.AssignRole(ctx, pool, user.ID, roleID))
	}

	custUUID, itemUUID := seedDocSendCustomerAndItem(t, pool)
	in := salesorder.CreateOrderInput{CustomerUUID: custUUID}
	in.Items = []salesorder.LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	order, err := salesorder.Create(ctx, pool, in, 1)
	require.NoError(t, err)

	so := NewSalesOrderOps(cp)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tenant/sales-orders/{uuid}/drawing", so.GetDrawing)
	mux.HandleFunc("PUT /api/tenant/sales-orders/{uuid}/drawing", so.PutDrawing)
	mux.HandleFunc("GET /api/tenant/sales-orders/{uuid}/drawing/versions", so.ListDrawingVersions)
	mux.HandleFunc("GET /api/tenant/sales-orders/{uuid}/drawing/versions/{version}", so.GetDrawingVersion)
	mux.HandleFunc("POST /api/tenant/sales-orders/{uuid}/drawing/versions/{version}/restore", so.RestoreDrawingVersion)
	mux.HandleFunc("GET /api/tenant/settings/edge-codes", GetEdgeCodes)
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	return &drawingTestEnv{t: t, mux: resolver.Middleware(mux), identityID: identity.ID, tenantID: tenant.ID, soUUID: order.ID}
}

func (e *drawingTestEnv) do(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	payload := middleware.UserContextPayload{ID: e.identityID, TenantID: e.tenantID}
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func soReadWrite() []authz.Grant {
	return []authz.Grant{
		{Resource: authz.ResourceSalesOrder, Action: authz.ActionRead, Scope: authz.ScopeAll},
		{Resource: authz.ResourceSalesOrder, Action: authz.ActionUpdate, Scope: authz.ScopeAll},
	}
}

func TestPutThenGetDrawing_DB(t *testing.T) {
	env := newDrawingTestEnv(t, soReadWrite()...)

	body := `{"areas":[` + validAreaJSON + `],"displayUnit":"in","fractionDenominator":32,"recordVersion":0}`
	rec := env.do(http.MethodPut, "/api/tenant/sales-orders/"+env.soUUID+"/drawing", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"version":1`)

	rec = env.do(http.MethodGet, "/api/tenant/sales-orders/"+env.soUUID+"/drawing", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"version":1`)
}

func TestPutDrawing_InvalidOutlineRejected_DB(t *testing.T) {
	env := newDrawingTestEnv(t, soReadWrite()...)
	body := `{"areas":[{"id":"a1","pieces":[{"id":"p1","outline":[{"x":0,"y":0},{"x":1,"y":1}],"edges":[],"seams":[],"cutouts":[]}],"cabinets":[],"notes":[]}],"displayUnit":"in","fractionDenominator":32,"recordVersion":0}`
	rec := env.do(http.MethodPut, "/api/tenant/sales-orders/"+env.soUUID+"/drawing", body)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestPutDrawing_StaleVersionReturns409_DB(t *testing.T) {
	env := newDrawingTestEnv(t, soReadWrite()...)
	body := `{"areas":[` + validAreaJSON + `],"displayUnit":"in","fractionDenominator":32,"recordVersion":0}`
	rec := env.do(http.MethodPut, "/api/tenant/sales-orders/"+env.soUUID+"/drawing", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Still claims version 0, but the server is now at version 1.
	rec = env.do(http.MethodPut, "/api/tenant/sales-orders/"+env.soUUID+"/drawing", body)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

// With zero sales_order:read grant, authSOByUUID's own resource-level check
// (authSO) denies with 403 before it ever looks up this specific record --
// the same 403 any uuid would get, so it leaks nothing about existence. The
// 404-masking IDOR guard is a separate, deeper check (recordInScope) for a
// caller who DOES hold an own/team-scoped grant but this record sits outside
// their scope; authSOByUUID's own tests already cover that path for every
// other endpoint built on it, and this endpoint reuses it unchanged.
func TestGetDrawing_WithoutAnyGrant_DeniesWithoutLeakingExistence_DB(t *testing.T) {
	env := newDrawingTestEnv(t) // no grants at all
	rec := env.do(http.MethodGet, "/api/tenant/sales-orders/"+env.soUUID+"/drawing", "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	rec = env.do(http.MethodGet, "/api/tenant/sales-orders/00000000-0000-0000-0000-000000000000/drawing", "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

func TestDrawingVersions_ListGetRestore_DB(t *testing.T) {
	env := newDrawingTestEnv(t, soReadWrite()...)
	v1Body := `{"areas":[` + validAreaJSON + `],"displayUnit":"in","fractionDenominator":32,"recordVersion":0}`
	rec := env.do(http.MethodPut, "/api/tenant/sales-orders/"+env.soUUID+"/drawing", v1Body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	v2Body := `{"areas":[` + validAreaJSON + `],"displayUnit":"mm","fractionDenominator":16,"recordVersion":1}`
	rec = env.do(http.MethodPut, "/api/tenant/sales-orders/"+env.soUUID+"/drawing", v2Body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = env.do(http.MethodGet, "/api/tenant/sales-orders/"+env.soUUID+"/drawing/versions", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"version":1`)
	assert.Contains(t, rec.Body.String(), `"version":2`)

	rec = env.do(http.MethodGet, "/api/tenant/sales-orders/"+env.soUUID+"/drawing/versions/1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"displayUnit":"in"`)

	// Restore v1 while claiming the current version (2) -> succeeds, becomes v3.
	rec = env.do(http.MethodPost, "/api/tenant/sales-orders/"+env.soUUID+"/drawing/versions/1/restore", `{"recordVersion":2}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"version":3`)
	assert.Contains(t, rec.Body.String(), `"displayUnit":"in"`) // v1's content, not v2's

	// Restoring again with a now-stale recordVersion conflicts.
	rec = env.do(http.MethodPost, "/api/tenant/sales-orders/"+env.soUUID+"/drawing/versions/1/restore", `{"recordVersion":2}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

func TestGetEdgeCodes_DB(t *testing.T) {
	env := newDrawingTestEnv(t) // no sales-order grants needed -- any authenticated user
	rec := env.do(http.MethodGet, "/api/tenant/settings/edge-codes", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"code":"EE"`)
}
