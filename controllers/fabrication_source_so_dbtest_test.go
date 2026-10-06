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

// fabricateAs seeds a caller holding grants plus one sales order, then POSTs
// the fabricate route for that order as the caller.
func fabricateAs(t *testing.T, grants ...authz.Grant) *httptest.ResponseRecorder {
	t.Helper()
	withTestJWTSecret(t)
	ctx := context.Background()
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
	for _, g := range grants {
		roleID := seedRBACTestRole(t, pool, "fab-src-so", g)
		require.NoError(t, authz.AssignRole(ctx, pool, user.ID, roleID))
	}

	custUUID, itemUUID := seedDocSendCustomerAndItem(t, pool)
	in := salesorder.CreateOrderInput{CustomerUUID: custUUID}
	in.Items = []salesorder.LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	order, err := salesorder.Create(ctx, pool, in, 1)
	require.NoError(t, err)
	// Job creation requires a confirmed order; salesorder.Create starts it as a
	// Draft, which would turn the success case into a 400.
	_, err = pool.Exec(ctx, `
		UPDATE sales_order SET sales_order_status = (
			SELECT record_status_id FROM lkp_record_status
			WHERE record_status_record_type = sales_order.record_type AND record_status_code = 'OPEN')
		WHERE sales_order_uuid = $1`, order.ID)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/tenant/sales-orders/{uuid}/fabricate", NewFabricationOps().Fabricate)
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	req := httptest.NewRequest(http.MethodPost, "/api/tenant/sales-orders/"+order.ID+"/fabricate", strings.NewReader("{}"))
	payload := middleware.UserContextPayload{ID: identity.ID, TenantID: tenant.ID}
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	resolver.Middleware(mux).ServeHTTP(rec, req)
	return rec
}

func installationCreate() authz.Grant {
	return authz.Grant{Resource: authz.ResourceInstallation, Action: authz.ActionCreate, Scope: authz.ScopeAll}
}

// installation:create alone must not let a caller reference a sales order they
// have no sales_order:read grant on.
func TestFabricate_RequiresSalesOrderRead_DB(t *testing.T) {
	rec := fabricateAs(t, installationCreate())
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// An own-scoped sales_order reader can't spawn a job from an order someone
// else owns; the order's existence is hidden (404, not 403).
func TestFabricate_OutOfScopeSalesOrderIs404_DB(t *testing.T) {
	rec := fabricateAs(t, installationCreate(),
		authz.Grant{Resource: authz.ResourceSalesOrder, Action: authz.ActionRead, Scope: authz.ScopeOwn})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// The legitimate path still works: sales_order:read with all scope.
func TestFabricate_AllScopeSalesOrderReadSucceeds_DB(t *testing.T) {
	rec := fabricateAs(t, installationCreate(),
		authz.Grant{Resource: authz.ResourceSalesOrder, Action: authz.ActionRead, Scope: authz.ScopeAll})
	assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}
