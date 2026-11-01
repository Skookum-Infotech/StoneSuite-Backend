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

// seedTxVendor inserts a live vendor owned by ownerEmployeeID (0 = unowned) and
// returns its uuid and internal id.
func seedTxVendor(t *testing.T, pool *pgxpool.Pool, ownerEmployeeID int) (string, int) {
	t.Helper()
	ctx := context.Background()
	var owner any
	if ownerEmployeeID != 0 {
		owner = ownerEmployeeID
	}
	var (
		uuid string
		id   int
	)
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_owner_id)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VNDR'),
		        (SELECT s.record_status_id FROM lkp_record_status s
		           JOIN lkp_record_type rt ON rt.record_type_id = s.record_status_record_type
		          WHERE rt.record_type_code = 'VNDR' AND s.record_status_code = 'ACT_'),
		        'Organization', $1, $2)
		RETURNING vendor_uuid::text, vendor_id`,
		fmt.Sprintf("VendorTx %d", time.Now().UnixNano()), owner).Scan(&uuid, &id))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM vendor WHERE vendor_id = $1`, id) })
	return uuid, id
}

// seedTxVendorBill inserts one approved bill for the vendor.
func seedTxVendorBill(t *testing.T, pool *pgxpool.Pool, vendorID int) {
	t.Helper()
	ctx := context.Background()
	var id int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO vendor_bill (record_type, vendor_bill_status, vendor_bill_vendor_id, vendor_bill_number,
		                         vendor_bill_date, vendor_bill_grand_total, vendor_bill_balance_due)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VBIL'),
		        (SELECT s.record_status_id FROM lkp_record_status s
		           JOIN lkp_record_type rt ON rt.record_type_id = s.record_status_record_type
		          WHERE rt.record_type_code = 'VBIL' AND s.record_status_code = 'APPV'),
		        $1, $2, CURRENT_DATE, 100, 100) RETURNING vendor_bill_id`,
		vendorID, fmt.Sprintf("VTX%d", time.Now().UnixNano()%1000000000000)).Scan(&id))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM vendor_bill WHERE vendor_bill_id = $1`, id) })
}

// callVendorTx drives a handler through the real tenant resolver as the caller.
func callVendorTx(t *testing.T, cp *tenancy.ControlPlane, handler http.HandlerFunc,
	tenantID, identityID, userID, vendorUUID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/vendors/"+vendorUUID+"/transactions"+query, nil)
	req.SetPathValue("uuid", vendorUUID)
	payload := middleware.UserContextPayload{ID: identityID, TenantID: tenantID, UserID: userID}
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	tenancy.NewResolver(cp, tenancy.NewRouter(nil)).Middleware(handler).ServeHTTP(rec, req)
	return rec
}

type vendorTxListResp struct {
	Success    bool   `json:"success"`
	NextCursor string `json:"nextCursor"`
	Rows       []struct {
		Type string `json:"type"`
	} `json:"rows"`
}

// TestVendorTransactions_IDOR_DB covers the guard chain in front of the tab:
// 404 for unknown and out-of-scope vendors, 403 without vendor:read, and no rows
// from a module the caller cannot read.
func TestVendorTransactions_IDOR_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	ops := NewVendorTransactionsOps(nil)
	ctx := context.Background()

	grant := func(res authz.Resource, scope authz.Scope) authz.Grant {
		return authz.Grant{Resource: res, Action: authz.ActionRead, Scope: scope}
	}
	// newCaller seeds an identity/user (with an employee profile) holding grants.
	newCaller := func(name string, grants ...authz.Grant) (identityID, userID string, employeeID int) {
		identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
		unique := fmt.Sprintf("vendortx-%s-%d", name, time.Now().UnixNano())
		roleID, err := authz.CreateRole(ctx, pool, unique, unique, "", grants)
		require.NoError(t, err)
		require.NoError(t, authz.AssignRole(ctx, pool, user.ID, roleID))
		require.NoError(t, pool.QueryRow(ctx, `
			INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
			VALUES ($1::uuid, 'VendorTx', $2, $3, 1)
			ON CONFLICT (employee_user_id) WHERE employee_user_id IS NOT NULL DO UPDATE SET employee_user_id = EXCLUDED.employee_user_id
			RETURNING employee_id`, user.ID, name, unique+"@vendortx.test").Scan(&employeeID))
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1::uuid`, user.ID)
			_, _ = pool.Exec(ctx, `DELETE FROM roles WHERE id = $1::uuid`, roleID)
			_, _ = pool.Exec(ctx, `DELETE FROM employee WHERE employee_id = $1`, employeeID)
		})
		return identity.ID, user.ID, employeeID
	}

	_, _, ownerEmp := newCaller("vendorowner", grant(authz.ResourceVendor, authz.ScopeAll))
	vendorUUID, vendorID := seedTxVendor(t, pool, ownerEmp)
	seedTxVendorBill(t, pool, vendorID)
	const missing = "00000000-0000-0000-0000-000000000000"

	t.Run("own-scoped vendor reader on a vendor they do not own is 404 on both routes", func(t *testing.T) {
		id, user, _ := newCaller("own", grant(authz.ResourceVendor, authz.ScopeOwn), grant(authz.ResourceVendorBill, authz.ScopeAll))
		for name, h := range map[string]http.HandlerFunc{"list": ops.List, "summary": ops.Summary} {
			rec := callVendorTx(t, cp, h, tenant.ID, id, user, vendorUUID, "")
			assert.Equal(t, http.StatusNotFound, rec.Code, name)
			assert.NotContains(t, rec.Body.String(), `"rows":[{`, name)
		}
	})

	t.Run("unknown uuid is 404", func(t *testing.T) {
		id, user, _ := newCaller("unk", grant(authz.ResourceVendor, authz.ScopeAll))
		for name, h := range map[string]http.HandlerFunc{"list": ops.List, "summary": ops.Summary} {
			rec := callVendorTx(t, cp, h, tenant.ID, id, user, missing, "")
			assert.Equal(t, http.StatusNotFound, rec.Code, name)
		}
	})

	t.Run("a vendor that exists only in another database resolves to 404", func(t *testing.T) {
		// Tenants are separate databases, so another tenant's vendor uuid simply
		// does not exist here: a well-formed uuid minted elsewhere behaves as unknown.
		id, user, _ := newCaller("xt", grant(authz.ResourceVendor, authz.ScopeAll), grant(authz.ResourceVendorBill, authz.ScopeAll))
		var foreign string
		require.NoError(t, pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&foreign))
		rec := callVendorTx(t, cp, ops.List, tenant.ID, id, user, foreign, "")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("no vendor read is 403", func(t *testing.T) {
		id, user, _ := newCaller("novendor", grant(authz.ResourceVendorBill, authz.ScopeAll))
		rec := callVendorTx(t, cp, ops.List, tenant.ID, id, user, vendorUUID, "")
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("vendor read without bill read lists no bills", func(t *testing.T) {
		id, user, _ := newCaller("nobill", grant(authz.ResourceVendor, authz.ScopeAll))
		rec := callVendorTx(t, cp, ops.List, tenant.ID, id, user, vendorUUID, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp vendorTxListResp
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.True(t, resp.Success)
		assert.Empty(t, resp.Rows)
	})

	t.Run("bad filters are 400", func(t *testing.T) {
		id, user, _ := newCaller("bad", grant(authz.ResourceVendor, authz.ScopeAll))
		for _, q := range []string{"?from=nope", "?type=expense", "?cursor=garbage", "?limit=-1"} {
			rec := callVendorTx(t, cp, ops.List, tenant.ID, id, user, vendorUUID, q)
			assert.Equal(t, http.StatusBadRequest, rec.Code, q)
		}
	})

	t.Run("reader with grants sees the bill and the summary agrees", func(t *testing.T) {
		id, user, _ := newCaller("ok", grant(authz.ResourceVendor, authz.ScopeAll), grant(authz.ResourceVendorBill, authz.ScopeAll))
		rec := callVendorTx(t, cp, ops.List, tenant.ID, id, user, vendorUUID, "?type=vendor_bill")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp vendorTxListResp
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Len(t, resp.Rows, 1)
		assert.Empty(t, resp.NextCursor)

		rec = callVendorTx(t, cp, ops.Summary, tenant.ID, id, user, vendorUUID, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var sum struct {
			Summary struct {
				OpenBalance *float64 `json:"openBalance"`
			} `json:"summary"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sum))
		require.NotNil(t, sum.Summary.OpenBalance)
		assert.InDelta(t, 100, *sum.Summary.OpenBalance, 0.001)
	})
}
