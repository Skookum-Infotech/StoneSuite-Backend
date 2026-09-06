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
	"stonesuite-backend/quote"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/vendors"
)

// seedGlobalSearchCustomer inserts a minimal live customer whose name contains
// term, mirroring quote/store_test.go's seedCustomerAndItem helper.
func seedGlobalSearchCustomer(t *testing.T, pool *pgxpool.Pool, term string) (custUUID string) {
	t.Helper()
	ctx := context.Background()
	var custTypeID int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`,
		custTypeID, "Customer "+term).Scan(&custUUID))
	return custUUID
}

// seedGlobalSearchItem inserts a minimal inventory item for quote line seeding.
func seedGlobalSearchItem(t *testing.T, pool *pgxpool.Pool, suffix string) (itemUUID string) {
	t.Helper()
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 1, 25.00, 1) RETURNING inventory_item_uuid`,
		"SKU-"+suffix, "Item "+suffix).Scan(&itemUUID))
	return itemUUID
}

// TestGlobalSearchOps_Search_DB is the end-to-end proof that (a) the
// customer/lead/prospect search fix works through the fan-out, (b) a caller
// only sees the groups their granted permissions allow, and (c) a term
// shorter than the minimum is rejected before any provider runs.
func TestGlobalSearchOps_Search_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	term := fmt.Sprintf("Zephyr%d", time.Now().UnixNano())

	custUUID := seedGlobalSearchCustomer(t, pool, term)
	itemUUID := seedGlobalSearchItem(t, pool, term)

	quoteIn := quote.CreateQuoteInput{CustomerUUID: custUUID}
	quoteIn.Items = []quote.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	_, err := quote.Create(context.Background(), pool, quoteIn, 1)
	require.NoError(t, err)

	vendorIn := vendors.CreateVendorInput{VendorType: "Organization"}
	vendorIn.LegalName = "Vendor " + term
	_, err = vendors.Create(context.Background(), pool, vendorIn, 1)
	require.NoError(t, err)

	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))
	gsOps := NewGlobalSearchOps()

	doSearch := func(t *testing.T, identityID, q string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/tenant/search?q="+q, nil)
		payload := middleware.UserContextPayload{ID: identityID, TenantID: tenant.ID}
		ctx := context.WithValue(req.Context(), middleware.UserContextKey, payload)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		resolver.Middleware(http.HandlerFunc(gsOps.Search)).ServeHTTP(rec, req)
		return rec
	}

	t.Run("term shorter than minimum is rejected", func(t *testing.T) {
		identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
		roleID := seedRBACTestRole(t, pool, "gs-short-term", authz.Grant{Resource: authz.ResourceQuote, Action: authz.ActionRead, Scope: authz.ScopeAll})
		require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

		rec := doSearch(t, identity.ID, "a")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("only granted modules appear in the response", func(t *testing.T) {
		identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
		roleID := seedRBACTestRole(t, pool, "gs-quote-only", authz.Grant{Resource: authz.ResourceQuote, Action: authz.ActionRead, Scope: authz.ScopeAll})
		require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

		rec := doSearch(t, identity.ID, term)
		require.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			Success bool                       `json:"success"`
			Groups  map[string]json.RawMessage `json:"groups"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.True(t, resp.Success)
		_, hasQuote := resp.Groups["quote"]
		_, hasVendor := resp.Groups["vendor"]
		_, hasCustomer := resp.Groups["customer"]
		assert.True(t, hasQuote, "expected a quote group since the caller has quote:read")
		assert.False(t, hasVendor, "did not expect a vendor group without vendor:read")
		assert.False(t, hasCustomer, "did not expect a customer group without customer:read")
	})

	t.Run("customer, quote and vendor all appear together once granted", func(t *testing.T) {
		identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw")
		roleID, err := authz.CreateRole(context.Background(), pool, fmt.Sprintf("gs-all-three-%d", time.Now().UnixNano()), "gs-all-three", "", []authz.Grant{
			{Resource: authz.ResourceQuote, Action: authz.ActionRead, Scope: authz.ScopeAll},
			{Resource: authz.ResourceVendor, Action: authz.ActionRead, Scope: authz.ScopeAll},
			{Resource: authz.ResourceCustomer, Action: authz.ActionRead, Scope: authz.ScopeAll},
		})
		require.NoError(t, err)
		require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

		rec := doSearch(t, identity.ID, term)
		require.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			Success bool `json:"success"`
			Groups  map[string]struct {
				Results []struct {
					DisplayName string `json:"displayName"`
				} `json:"results"`
				HasMore bool `json:"hasMore"`
			} `json:"groups"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.True(t, resp.Success)

		require.Contains(t, resp.Groups, "quote")
		assert.NotEmpty(t, resp.Groups["quote"].Results)

		require.Contains(t, resp.Groups, "vendor")
		assert.NotEmpty(t, resp.Groups["vendor"].Results, "vendor search should now be reachable through global search")

		require.Contains(t, resp.Groups, "customer", "customer group must appear now that its SearchPredicate is fixed")
		assert.NotEmpty(t, resp.Groups["customer"].Results, "the customer/lead/prospect search fix must surface matching customers end-to-end")
	})
}
