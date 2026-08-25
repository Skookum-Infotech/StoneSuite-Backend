//go:build dbtest

package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/middleware"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
	"stonesuite-backend/workflow"
)

// This test exercises Send end-to-end through the *real* production
// middleware chain (tenancy.Resolver.Middleware), not a hand-built context.
// tenancy.TenantFromContext/PoolFromContext read an unexported context key
// that only Resolver.Middleware can populate, so a genuine (non-t.Skip) HTTP
// test of a /api/tenant/records/{id}/... handler needs BOTH a control-plane
// database (TEST_CP_DATABASE_URL, for tenant resolution) and a tenant
// database (TEST_DATABASE_URL, for the record + RBAC data) -- unlike the
// store-level dbtests elsewhere in this repo (e.g.
// workflow/document_sends_dbtest_test.go) that call store functions directly
// and only ever need the latter. Skips cleanly when either is unset.

// docSendTestControlPlane connects to the control-plane test database,
// mirroring saml_dbtest_test.go's newSAMLTestControlPlane.
func docSendTestControlPlane(t *testing.T) *tenancy.ControlPlane {
	t.Helper()
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping dbtest")
	}
	cp, err := tenancy.NewControlPlane(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(cp.Close)
	return cp
}

// docSendTestTenantDSN returns the tenant database DSN, skipping cleanly when
// it is not configured (mirrors workflow/document_sends_dbtest_test.go).
func docSendTestTenantDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping dbtest")
	}
	return dsn
}

// seedDocSendCustomerAndItem inserts a minimal live customer + inventory_item,
// mirroring salesorder/store_test.go's seedCustomerAndItem helper (unexported
// there, so re-declared here for this package's dbtest).
func seedDocSendCustomerAndItem(t *testing.T, pool *pgxpool.Pool) (custUUID, itemUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID))

	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`,
		custTypeID, "Doc Send Test Customer "+suffix).Scan(&custUUID))

	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 1, 25.00, 1) RETURNING inventory_item_uuid`,
		"DOCSEND-SKU-"+suffix, "Doc Send Test Item "+suffix).Scan(&itemUUID))

	return custUUID, itemUUID
}

// TestDocumentOps_Send_HappyPath_DB seeds a tenant + sales order and drives a
// real authed POST .../document/send through the production middleware
// chain, asserting the PDF was rendered and emailed (no persistence step)
// and the send was recorded.
//
// sales_order is used as the seeded record type (not invoice, per the task
// brief) because workflow.ResolveRecordAccess -- the shared RBAC+IDOR gate
// authRecordAccess/loadForRender both depend on -- has no branch for
// invoice/quote/estimate records; only v1 workflow_records, the v2 customer
// table, and sales_order (among the four document modules) resolve. Seeding
// an invoice here would 404 at the auth gate regardless of anything Send
// does, which would make this "real" test assert nothing meaningful. See the
// task report for this gap flagged as a separate follow-up.
func TestDocumentOps_Send_HappyPath_DB(t *testing.T) {
	cp := docSendTestControlPlane(t)
	tenantDSN := docSendTestTenantDSN(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	tenant, err := cp.CreateTenant(ctx, "docsend-test-"+suffix, "Doc Send Test Tenant", false)
	require.NoError(t, err)
	require.NoError(t, cp.SetTenantProvisioned(ctx, tenant.ID, "docsend-test-db", tenantDSN, 1))
	tenant, err = cp.TenantByID(ctx, tenant.ID)
	require.NoError(t, err)

	router := tenancy.NewRouter(nil) // nil -> PlainDSNResolver (DBConnectionRef is used as-is)
	t.Cleanup(router.Close)
	pool, err := router.PoolFor(ctx, tenant)
	require.NoError(t, err)

	custUUID, itemUUID := seedDocSendCustomerAndItem(t, pool)
	in := salesorder.CreateOrderInput{CustomerUUID: custUUID}
	in.SalesTaxPercent = 8
	in.Items = []salesorder.LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2, DiscountPercent: 0}}
	order, err := salesorder.Create(ctx, pool, in, 1)
	require.NoError(t, err)

	identityID, err := newAttachUUID()
	require.NoError(t, err)
	usr, err := userstore.CreateUser(ctx, pool, identityID, "sender-"+suffix+"@example.com", "Sender", "active")
	require.NoError(t, err)
	require.NoError(t, authz.SeedTenantRBAC(ctx, pool, usr.ID))

	docOps := NewDocumentOps(map[string]DocumentLoader{
		"sales_order": func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error) {
			so, err := salesorder.Get(ctx, pool, uuid)
			if err != nil {
				return docpdf.PrintableDoc{}, DocMeta{}, fmt.Errorf("load sales order: %w", err)
			}
			email, name := salesorder.Recipient(*so)
			return salesorder.ToPrintable(*so, seller), DocMeta{
				WorkflowKey: "sales_order", Number: so.Number,
				DefaultRecipientEmail: email, DefaultRecipientName: name,
				DefaultSubject: "Your Sales Order " + so.Number,
			}, nil
		},
	})
	docOps.renderPDF = func(docpdf.PrintableDoc) ([]byte, error) { return []byte("%PDF-1.4 x"), nil }

	resolver := tenancy.NewResolver(cp, router)
	handler := resolver.Middleware(http.HandlerFunc(docOps.Send))

	body := `{"to":["bob@buyer.example"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/records/"+order.ID+"/document/send", strings.NewReader(body))
	req.SetPathValue("id", order.ID)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
		middleware.UserContextPayload{ID: identityID, TenantID: tenant.ID}))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	sends, err := workflow.ListDocumentSends(ctx, pool, order.ID)
	require.NoError(t, err)
	require.Len(t, sends, 1)
	assert.Equal(t, "sales_order", sends[0].WorkflowKey)
	assert.Equal(t, "bob@buyer.example", sends[0].SentTo)
	assert.Empty(t, sends[0].AttachmentID, "no attachment is persisted; the PDF is emailed directly")
}
