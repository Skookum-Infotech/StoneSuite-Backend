//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// seedTxCRMRecord inserts a CRM record of the given type code ("LEAD", "CUST")
// owned by ownerUserID (may be empty) and returns its uuid and internal id.
func seedTxCRMRecord(t *testing.T, pool *pgxpool.Pool, typeCode, ownerUserID string) (string, int) {
	t.Helper()
	ctx := context.Background()
	var (
		uuid string
		id   int
	)
	var owner any
	if ownerUserID != "" {
		// customer_crm_owner_user_id references employee(employee_id): resolve
		// (or create) the employee row that belongs to the owning users.id.
		var empID int
		require.NoError(t, pool.QueryRow(ctx, `
			INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
			VALUES ($1::uuid, 'CustTx', 'Owner', $2, 1)
			ON CONFLICT (employee_user_id) WHERE employee_user_id IS NOT NULL DO UPDATE SET employee_user_id = EXCLUDED.employee_user_id
			RETURNING employee_id`,
			ownerUserID, fmt.Sprintf("owner-%d@custtx.test", time.Now().UnixNano())).Scan(&empID))
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM employee WHERE employee_id = $1`, empID) })
		owner = empID
	}
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by, customer_crm_owner_user_id)
		SELECT record_type_id, $2, 1, $3::int FROM lkp_record_type WHERE record_type_code = $1
		RETURNING customer_uuid::text, customer_id`,
		typeCode, fmt.Sprintf("CustTx %s %d", typeCode, time.Now().UnixNano()), owner).Scan(&uuid, &id))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM customer WHERE customer_id = $1`, id)
	})
	return uuid, id
}

// seedTxInvoice inserts one sent invoice for the customer and returns its id.
func seedTxInvoice(t *testing.T, pool *pgxpool.Pool, customerID int) {
	t.Helper()
	ctx := context.Background()
	var id int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO invoice (record_type, invoice_status, invoice_customer_id, invoice_number, invoice_date,
		                     invoice_grand_total, invoice_balance_due)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'INVC'),
		        (SELECT s.record_status_id FROM lkp_record_status s
		           JOIN lkp_record_type rt ON rt.record_type_id = s.record_status_record_type
		          WHERE rt.record_type_code = 'INVC' AND s.record_status_code = 'SENT'),
		        $1, $2, CURRENT_DATE, 100, 100) RETURNING invoice_id`,
		customerID, fmt.Sprintf("CTX%d", time.Now().UnixNano()%1000000000000)).Scan(&id))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM invoice WHERE invoice_id = $1`, id) })
}

// callCustomerTx drives a handler through the real tenant resolver as the caller.
func callCustomerTx(t *testing.T, cp *tenancy.ControlPlane, handler http.HandlerFunc,
	tenantID, identityID, userID, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetPathValue("id", pathRecordID(path))
	payload := middleware.UserContextPayload{ID: identityID, TenantID: tenantID, UserID: userID}
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	tenancy.NewResolver(cp, tenancy.NewRouter(nil)).Middleware(handler).ServeHTTP(rec, req)
	return rec
}

// pathRecordID extracts the record id from /api/tenant/crm/customer/records/{id}/....
func pathRecordID(path string) string {
	const prefix = "/api/tenant/crm/customer/records/"
	rest := path[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' || rest[i] == '?' {
			return rest[:i]
		}
	}
	return rest
}

type txListResp struct {
	Success bool `json:"success"`
	Total   int  `json:"total"`
	Rows    []struct {
		Type string `json:"type"`
	} `json:"rows"`
}

// TestCustomerTransactions_IDOR_DB covers the guard chain in front of the tab.
// Deviation from the plan: a caller without customer:read gets 403 from the
// shared authCRMByRecordID helper, as every other CRM sub-resource does.
func TestCustomerTransactions_IDOR_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	ops := NewCustomerTransactionsOps(nil)

	_, other := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")

	grant := func(res authz.Resource, scope authz.Scope) authz.Grant {
		return authz.Grant{Resource: res, Action: authz.ActionRead, Scope: scope}
	}
	newCaller := func(name string, grants ...authz.Grant) (string, string) {
		identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
		unique := fmt.Sprintf("custtx-%s-%d", name, time.Now().UnixNano())
		roleID, err := authz.CreateRole(context.Background(), pool, unique, unique, "", grants)
		require.NoError(t, err)
		require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM user_roles WHERE user_id = $1::uuid`, user.ID)
			_, _ = pool.Exec(context.Background(), `DELETE FROM roles WHERE id = $1::uuid`, roleID)
		})
		return identity.ID, user.ID
	}

	custUUID, custID := seedTxCRMRecord(t, pool, "CUST", other.ID)
	seedTxInvoice(t, pool, custID)
	leadUUID, _ := seedTxCRMRecord(t, pool, "LEAD", "")
	base := "/api/tenant/crm/customer/records/"

	t.Run("own-scoped caller on a customer they do not own", func(t *testing.T) {
		id, user := newCaller("own", grant(authz.ResourceCustomer, authz.ScopeOwn), grant(authz.ResourceInvoice, authz.ScopeAll))
		for _, suffix := range []string{"/transactions", "/transactions/summary"} {
			h := ops.List
			if suffix != "/transactions" {
				h = ops.Summary
			}
			rec := callCustomerTx(t, cp, h, tenant.ID, id, user, base+custUUID+suffix)
			assert.Equal(t, http.StatusNotFound, rec.Code, suffix)
			assert.NotContains(t, rec.Body.String(), "rows\":[{")
		}
	})

	t.Run("lead uuid is not a customer", func(t *testing.T) {
		id, user := newCaller("lead", grant(authz.ResourceCustomer, authz.ScopeAll), grant(authz.ResourceLead, authz.ScopeAll))
		rec := callCustomerTx(t, cp, ops.List, tenant.ID, id, user, base+leadUUID+"/transactions")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("lead uuid without lead read is 404 not 403", func(t *testing.T) {
		id, user := newCaller("leadnoread", grant(authz.ResourceCustomer, authz.ScopeAll))
		rec := callCustomerTx(t, cp, ops.List, tenant.ID, id, user, base+leadUUID+"/transactions")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("unknown uuid", func(t *testing.T) {
		id, user := newCaller("unk", grant(authz.ResourceCustomer, authz.ScopeAll))
		rec := callCustomerTx(t, cp, ops.List, tenant.ID, id, user, base+"00000000-0000-0000-0000-000000000000/transactions")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("no customer read", func(t *testing.T) {
		id, user := newCaller("nocust", grant(authz.ResourceInvoice, authz.ScopeAll))
		rec := callCustomerTx(t, cp, ops.List, tenant.ID, id, user, base+custUUID+"/transactions")
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("customer read without invoice read lists no invoices", func(t *testing.T) {
		id, user := newCaller("noinv", grant(authz.ResourceCustomer, authz.ScopeAll))
		rec := callCustomerTx(t, cp, ops.List, tenant.ID, id, user, base+custUUID+"/transactions")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp txListResp
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.True(t, resp.Success)
		assert.Empty(t, resp.Rows)
	})

	t.Run("bad from date", func(t *testing.T) {
		id, user := newCaller("badfrom", grant(authz.ResourceCustomer, authz.ScopeAll))
		rec := callCustomerTx(t, cp, ops.List, tenant.ID, id, user, base+custUUID+"/transactions?from=nope")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("reader with grants sees the invoice", func(t *testing.T) {
		id, user := newCaller("ok", grant(authz.ResourceCustomer, authz.ScopeAll), grant(authz.ResourceInvoice, authz.ScopeAll))
		rec := callCustomerTx(t, cp, ops.List, tenant.ID, id, user, base+custUUID+"/transactions?type=invoice")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp txListResp
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, 1, resp.Total)
	})
}
