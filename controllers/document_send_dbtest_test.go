//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/config"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/middleware"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/vendorcredit"
	"stonesuite-backend/vendorpayment"
	"stonesuite-backend/vendors"
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
	}, nil)
	docOps.renderPDF = func(docpdf.PrintableDoc) ([]byte, error) { return []byte("%PDF-1.4 x"), nil }

	// Send() now emails the customer copy via the Notify service
	// (services.SendNotification), not a direct Resend/SMTP call -- stub it
	// out the same way services/notify_test.go does, since CI runs this
	// dbtest without a live Notify service. Capture each request body so the
	// test can assert the recipient/actor ids are control-plane identity ids,
	// never the tenant users.id (a bell row keyed by users.id is one
	// stonesuite-notify can never find).
	type capturedNotify struct {
		ActorUserID string `json:"actorUserId"`
		Recipients  []struct {
			UserID string `json:"userId"`
			Email  string `json:"email"`
		} `json:"recipients"`
	}
	var (
		notifyMu       sync.Mutex
		notifyRequests []capturedNotify
	)
	notifyStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body capturedNotify
		_ = json.NewDecoder(r.Body).Decode(&body)
		notifyMu.Lock()
		notifyRequests = append(notifyRequests, body)
		notifyMu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"notifications":[{"id":"notif-abc"}]}}`))
	}))
	t.Cleanup(notifyStub.Close)
	config.AppConfig = config.Config{NotifyURL: notifyStub.URL, NotifyAPIKey: "nk_dbtest_stub_secret"}

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
	assert.Equal(t, []string{"notif-abc"}, sends[0].NotifyNotificationIDs,
		"notify's returned notification ids are persisted for later delivery-status lookup")

	notifyMu.Lock()
	defer notifyMu.Unlock()
	require.NotEmpty(t, notifyRequests, "Send must call the notify service")
	assert.Equal(t, identityID, notifyRequests[0].ActorUserID,
		"the customer-copy notification's actor must be the control-plane identity id, not the tenant users.id")
	for _, nr := range notifyRequests {
		assert.NotEqual(t, usr.ID, nr.ActorUserID, "actor id must not be the tenant users.id")
		for _, rcpt := range nr.Recipients {
			assert.NotEqual(t, usr.ID, rcpt.UserID,
				"a recipient keyed by users.id is a bell row stonesuite-notify can never find")
			if rcpt.UserID != "" {
				assert.Equal(t, identityID, rcpt.UserID,
					"the owner-ping recipient must be the owner's control-plane identity id")
			}
		}
	}
}

// TestDocumentOps_Send_VendorModules_DB is the purchase-side counterpart to
// TestDocumentOps_Send_HappyPath_DB: it seeds one vendor with an email on
// file, creates one record per newly-wired workflow key (purchase_order,
// vendor_bill, vendor_credit, vendor_payment), and drives Send with an EMPTY
// recipient list -- exercising the actual value-add of this feature, that
// Recipient() resolves the vendor's email via a vendors.Get lookup (none of
// these four modules snapshot an email on the record itself, unlike
// sales_order's Billing.Email). Unlike the happy-path test above, this
// doesn't re-assert the notify actor/recipient-id plumbing (unchanged by this
// feature, already covered there) -- it only asserts each workflow key
// resolves, dispatches, and records a send to the vendor's email.
func TestDocumentOps_Send_VendorModules_DB(t *testing.T) {
	cp := docSendTestControlPlane(t)
	tenantDSN := docSendTestTenantDSN(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	tenant, err := cp.CreateTenant(ctx, "docsend-vendor-test-"+suffix, "Doc Send Vendor Test Tenant", false)
	require.NoError(t, err)
	require.NoError(t, cp.SetTenantProvisioned(ctx, tenant.ID, "docsend-vendor-test-db", tenantDSN, 1))
	tenant, err = cp.TenantByID(ctx, tenant.ID)
	require.NoError(t, err)

	router := tenancy.NewRouter(nil)
	t.Cleanup(router.Close)
	pool, err := router.PoolFor(ctx, tenant)
	require.NoError(t, err)

	_, itemUUID := seedDocSendCustomerAndItem(t, pool)

	vendorEmail := "vendor-" + suffix + "@example.com"
	vIn := vendors.CreateVendorInput{VendorType: "Organization"}
	vIn.Email = vendorEmail
	vIn.LegalName = "Doc Send Test Vendor " + suffix
	vendor, err := vendors.Create(ctx, pool, vIn, 1)
	require.NoError(t, err)

	var methodID int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_deleted_at IS NULL ORDER BY payment_method_id LIMIT 1`,
	).Scan(&methodID))

	poIn := purchaseorder.CreatePurchaseOrderInput{VendorUUID: vendor.ID}
	poIn.Items = []purchaseorder.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	po, err := purchaseorder.Create(ctx, pool, poIn, 1)
	require.NoError(t, err)

	vbIn := vendorbill.CreateVendorBillInput{VendorUUID: vendor.ID}
	vbIn.Items = []vendorbill.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	vb, err := vendorbill.Create(ctx, pool, vbIn, 1)
	require.NoError(t, err)

	vc, err := vendorcredit.Create(ctx, pool, vendorcredit.CreateVendorCreditInput{VendorUUID: vendor.ID, Amount: 50}, 1)
	require.NoError(t, err)

	vp, err := vendorpayment.Create(ctx, pool, vendorpayment.CreateVendorPaymentInput{VendorUUID: vendor.ID, MethodID: methodID, Amount: 75}, 1)
	require.NoError(t, err)

	identityID, err := newAttachUUID()
	require.NoError(t, err)
	usr, err := userstore.CreateUser(ctx, pool, identityID, "sender-vendor-"+suffix+"@example.com", "Sender", "active")
	require.NoError(t, err)
	require.NoError(t, authz.SeedTenantRBAC(ctx, pool, usr.ID))

	docOps := NewDocumentOps(map[string]DocumentLoader{
		"purchase_order": func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error) {
			po, err := purchaseorder.Get(ctx, pool, uuid)
			if err != nil {
				return docpdf.PrintableDoc{}, DocMeta{}, fmt.Errorf("load purchase order: %w", err)
			}
			email, name := purchaseorder.Recipient(ctx, pool, *po)
			return purchaseorder.ToPrintable(*po, seller), DocMeta{
				WorkflowKey: "purchase_order", Number: po.Number,
				DefaultRecipientEmail: email, DefaultRecipientName: name,
				DefaultSubject: "Purchase Order " + po.Number,
			}, nil
		},
		"vendor_bill": func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error) {
			vb, err := vendorbill.Get(ctx, pool, uuid)
			if err != nil {
				return docpdf.PrintableDoc{}, DocMeta{}, fmt.Errorf("load vendor bill: %w", err)
			}
			email, name := vendorbill.Recipient(ctx, pool, *vb)
			return vendorbill.ToPrintable(*vb, seller), DocMeta{
				WorkflowKey: "vendor_bill", Number: vb.Number,
				DefaultRecipientEmail: email, DefaultRecipientName: name,
				DefaultSubject: "Vendor Bill " + vb.Number,
			}, nil
		},
		"vendor_credit": func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error) {
			vc, err := vendorcredit.Get(ctx, pool, uuid)
			if err != nil {
				return docpdf.PrintableDoc{}, DocMeta{}, fmt.Errorf("load vendor credit: %w", err)
			}
			email, name := vendorcredit.Recipient(ctx, pool, *vc)
			return vendorcredit.ToPrintable(*vc, seller), DocMeta{
				WorkflowKey: "vendor_credit", Number: vc.Number,
				DefaultRecipientEmail: email, DefaultRecipientName: name,
				DefaultSubject: "Vendor Credit " + vc.Number,
			}, nil
		},
		"vendor_payment": func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error) {
			vp, err := vendorpayment.Get(ctx, pool, uuid)
			if err != nil {
				return docpdf.PrintableDoc{}, DocMeta{}, fmt.Errorf("load vendor payment: %w", err)
			}
			email, name := vendorpayment.Recipient(ctx, pool, *vp)
			return vendorpayment.ToPrintable(*vp, seller), DocMeta{
				WorkflowKey: "vendor_payment", Number: vp.Number,
				DefaultRecipientEmail: email, DefaultRecipientName: name,
				DefaultSubject: "Vendor Payment " + vp.Number,
			}, nil
		},
	}, nil)
	docOps.renderPDF = func(docpdf.PrintableDoc) ([]byte, error) { return []byte("%PDF-1.4 x"), nil }

	notifyStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"notifications":[{"id":"notif-vendor"}]}}`))
	}))
	t.Cleanup(notifyStub.Close)
	config.AppConfig = config.Config{NotifyURL: notifyStub.URL, NotifyAPIKey: "nk_dbtest_stub_secret"}

	resolver := tenancy.NewResolver(cp, router)
	handler := resolver.Middleware(http.HandlerFunc(docOps.Send))

	cases := []struct {
		workflowKey string
		recordID    string
	}{
		{"purchase_order", po.ID},
		{"vendor_bill", vb.ID},
		{"vendor_credit", vc.ID},
		{"vendor_payment", vp.ID},
	}
	for _, tc := range cases {
		t.Run(tc.workflowKey, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/tenant/records/"+tc.recordID+"/document/send", strings.NewReader(""))
			req.SetPathValue("id", tc.recordID)
			req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
				middleware.UserContextPayload{ID: identityID, TenantID: tenant.ID}))
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

			sends, err := workflow.ListDocumentSends(ctx, pool, tc.recordID)
			require.NoError(t, err)
			require.Len(t, sends, 1)
			assert.Equal(t, tc.workflowKey, sends[0].WorkflowKey)
			assert.Equal(t, vendorEmail, sends[0].SentTo,
				"Recipient() must resolve the vendor's email via vendors.Get since the record itself carries no email snapshot")
		})
	}
}
