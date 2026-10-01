//go:build dbtest

package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/middleware"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
	"stonesuite-backend/workflow"
)

// poResendEnv is a servable tenant with one Draft purchase order, a sender who
// holds every grant, and the real ResendToVendor handler behind the resolver.
type poResendEnv struct {
	pool    *pgxpool.Pool
	handler http.Handler
	orderID string
	request func() *http.Request
}

func newPOResendEnv(t *testing.T) *poResendEnv {
	t.Helper()
	cp := docSendTestControlPlane(t)
	tenantDSN := docSendTestTenantDSN(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	tenant, err := cp.CreateTenant(ctx, "po-resend-"+suffix, "PO Resend Tenant", false)
	require.NoError(t, err)
	require.NoError(t, cp.SetTenantProvisioned(ctx, tenant.ID, "po-resend-db", tenantDSN, 1))
	tenant, err = cp.TenantByID(ctx, tenant.ID)
	require.NoError(t, err)

	router := tenancy.NewRouter(nil)
	t.Cleanup(router.Close)
	pool, err := router.PoolFor(ctx, tenant)
	require.NoError(t, err)

	var vndrTypeID, activeStatusID int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VNDR'`).Scan(&vndrTypeID))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'ACT_'`, vndrTypeID).Scan(&activeStatusID))
	var vendorUUID, itemUUID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		VALUES ($1, $2, 'Organization', $3, 1) RETURNING vendor_uuid`,
		vndrTypeID, activeStatusID, "Resend Vendor "+suffix).Scan(&vendorUUID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 1, 25.00, 1) RETURNING inventory_item_uuid`,
		"RESEND-SKU-"+suffix, "Resend Item "+suffix).Scan(&itemUUID))

	in := purchaseorder.CreatePurchaseOrderInput{VendorUUID: vendorUUID}
	in.Items = []purchaseorder.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	order, err := purchaseorder.Create(ctx, pool, in, 1)
	require.NoError(t, err)

	identityID, err := newAttachUUID()
	require.NoError(t, err)
	usr, err := userstore.CreateUser(ctx, pool, identityID, "po-sender-"+suffix+"@example.com", "PO Sender", "active")
	require.NoError(t, err)
	require.NoError(t, authz.SeedTenantRBAC(ctx, pool, usr.ID))

	docOps := NewDocumentOps(map[string]DocumentLoader{
		poWorkflowKey: func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error) {
			po, err := purchaseorder.Get(ctx, pool, uuid)
			if err != nil {
				return docpdf.PrintableDoc{}, DocMeta{}, fmt.Errorf("load purchase order: %w", err)
			}
			return purchaseorder.ToPrintable(*po, seller),
				DocMeta{WorkflowKey: poWorkflowKey, Number: po.Number, DefaultSubject: "Purchase Order " + po.Number}, nil
		},
	}, map[string]bool{poWorkflowKey: true}, nil)
	docOps.renderPDF = func(docpdf.PrintableDoc) ([]byte, error) { return []byte("%PDF-1.4 x"), nil }
	poOps := NewPurchaseOrderOps().WithVendorSender(docOps)

	return &poResendEnv{
		pool:    pool,
		handler: tenancy.NewResolver(cp, router).Middleware(http.HandlerFunc(poOps.ResendToVendor)),
		orderID: order.ID,
		request: func() *http.Request {
			req := httptest.NewRequest(http.MethodPost, "/api/tenant/purchase-orders/"+order.ID+"/resend",
				strings.NewReader(`{"to":["vendor@supplier.example"]}`))
			req.SetPathValue("uuid", order.ID)
			return req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
				middleware.UserContextPayload{ID: identityID, TenantID: tenant.ID}))
		},
	}
}

// TestPurchaseOrderResendToVendor_DB: a Draft order is refused (the generic
// send is disabled for purchase orders precisely so an unapproved order is
// never mailed), a Sent order is re-emailed and the send recorded, and the
// resend never changes the order's status.
func TestPurchaseOrderResendToVendor_DB(t *testing.T) {
	env := newPOResendEnv(t)
	ctx := context.Background()
	withFakeNotify(t, notifyScenario{emailStatus: "sent"})

	t.Run("a draft order is refused and nothing is sent", func(t *testing.T) {
		rr := httptest.NewRecorder()
		env.handler.ServeHTTP(rr, env.request())

		assert.Equal(t, http.StatusConflict, rr.Code, "body: %s", rr.Body.String())
		assert.Contains(t, rr.Body.String(), "already been sent to the vendor")
		sends, err := workflow.ListDocumentSends(ctx, env.pool, env.orderID)
		require.NoError(t, err)
		assert.Empty(t, sends)
	})

	t.Run("a sent order is emailed again, recorded, and keeps its status", func(t *testing.T) {
		moved, err := purchaseorder.Transition(ctx, env.pool, env.orderID, purchaseorder.SentStatusCode, 1)
		require.NoError(t, err)
		require.Equal(t, purchaseorder.SentStatusCode, moved.StatusCode, "test setup: order must be Sent")

		rr := httptest.NewRecorder()
		env.handler.ServeHTTP(rr, env.request())

		require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
		assertEmailOutcome(t, rr.Body.String(), true, "")
		assert.Contains(t, rr.Body.String(), "vendor@supplier.example")
		sends, err := workflow.ListDocumentSends(ctx, env.pool, env.orderID)
		require.NoError(t, err)
		require.Len(t, sends, 1)
		assert.Equal(t, "vendor@supplier.example", sends[0].SentTo)

		after, err := purchaseorder.Get(ctx, env.pool, env.orderID)
		require.NoError(t, err)
		assert.Equal(t, purchaseorder.SentStatusCode, after.StatusCode, "a resend must not move the order")
	})

	t.Run("a second resend is recorded as its own send", func(t *testing.T) {
		rr := httptest.NewRecorder()
		env.handler.ServeHTTP(rr, env.request())

		require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
		sends, err := workflow.ListDocumentSends(ctx, env.pool, env.orderID)
		require.NoError(t, err)
		assert.Len(t, sends, 2)
	})
}
