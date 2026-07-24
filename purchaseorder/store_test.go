// purchaseorder/store_test.go
//go:build dbtest

package purchaseorder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/query"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedVendorAndItem inserts a minimal live vendor + inventory_item, mirroring
// estimate/store_test.go's seedCustomerAndItem for the Purchases side.
func seedVendorAndItem(t *testing.T, pool *pgxpool.Pool) (vendorUUID, itemUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var vndrTypeID, activeStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VNDR'`).Scan(&vndrTypeID); err != nil {
		t.Fatalf("resolve VNDR record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'ACT_'`, vndrTypeID).Scan(&activeStatusID); err != nil {
		t.Fatalf("resolve vendor ACT_ status: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		VALUES ($1, $2, 'Organization', $3, 1) RETURNING vendor_uuid`,
		vndrTypeID, activeStatusID, "Test Vendor "+suffix).Scan(&vendorUUID); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 1, 25.00, 1) RETURNING inventory_item_uuid`,
		"POSKU-"+suffix, "Test PO Item "+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	return vendorUUID, itemUUID
}

func TestCreate_SnapshotsAndTotals(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)

	in := CreatePurchaseOrderInput{
		VendorUUID: vendorUUID,
		purchaseOrderFields: purchaseOrderFields{
			SalesTaxPercent: 8,
			Items: []LineInput{
				{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2, DiscountPercent: 0},
			},
		},
	}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Number == "" || got.Number[:5] != "PORD-" {
		t.Errorf("Number = %q, want PORD- prefix", got.Number)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}
	if got.Vendor.Name == "" {
		t.Errorf("Vendor.Name not snapshotted")
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
	// unit price snapshotted from inventory_item (25.00) since request left UnitPrice at 0.
	if got.Items[0].UnitPrice != 25 {
		t.Errorf("Items[0].UnitPrice = %v, want 25", got.Items[0].UnitPrice)
	}
	if got.Items[0].QtyReceived != 0 {
		t.Errorf("Items[0].QtyReceived = %v, want 0", got.Items[0].QtyReceived)
	}
	// subtotal = 2*25=50, tax = 50*0.08=4, grand = 54
	if got.GrandTotal != 54 {
		t.Errorf("GrandTotal = %v, want 54", got.GrandTotal)
	}
}

func TestCreate_RequiresVendor(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreatePurchaseOrderInput{}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with no vendor = %v, want ClientError", err)
	}
}

func TestCreate_FreeFormLine(t *testing.T) {
	pool := testPool(t)
	vendorUUID, _ := seedVendorAndItem(t, pool)

	got, err := Create(context.Background(), pool, CreatePurchaseOrderInput{
		VendorUUID: vendorUUID,
		purchaseOrderFields: purchaseOrderFields{
			Items: []LineInput{{LineNumber: 1, Description: "Custom fabrication service", Quantity: 1, UnitPrice: 500}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create free-form: %v", err)
	}
	if got.Items[0].ItemName != "Custom fabrication service" {
		t.Errorf("free-form ItemName = %q, want the description", got.Items[0].ItemName)
	}
	if got.GrandTotal != 500 {
		t.Errorf("GrandTotal = %v, want 500", got.GrandTotal)
	}
}

func TestUpdate_OnlyDraftEditable(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreatePurchaseOrderInput{
		VendorUUID:          vendorUUID,
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Draft update recomputes totals.
	updated, err := Update(ctx, pool, created.ID, UpdatePurchaseOrderInput{
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 3}}},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.GrandTotal != 75 {
		t.Errorf("GrandTotal after update = %v, want 75", updated.GrandTotal)
	}

	// Past draft, update is rejected (AD-10).
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	_, err = Update(ctx, pool, created.ID, UpdatePurchaseOrderInput{
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 5}}},
	}, 1)
	if !IsClientError(err) {
		t.Fatalf("Update at PAPV = %v, want ClientError", err)
	}
}

func TestSoftDelete_GuardedByStatus(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreatePurchaseOrderInput{
		VendorUUID:          vendorUUID,
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A submitted order cannot be deleted (AD-9)...
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	if err := SoftDelete(ctx, pool, created.ID, 1); !IsClientError(err) {
		t.Fatalf("SoftDelete at PAPV = %v, want ClientError", err)
	}

	// ...but a draft can.
	if _, err := Transition(ctx, pool, created.ID, "DRFT", 1); err != nil {
		t.Fatalf("Transition back to DRFT: %v", err)
	}
	if err := SoftDelete(ctx, pool, created.ID, 1); err != nil {
		t.Fatalf("SoftDelete at DRFT: %v", err)
	}
	if _, err := Get(ctx, pool, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestTransition_RejectsIllegalMove(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreatePurchaseOrderInput{
		VendorUUID:          vendorUUID,
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(context.Background(), pool, created.ID, "SENT", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition DRFT->SENT = %v, want ErrInvalidTransition", err)
	}
}

func TestApprove_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreatePurchaseOrderInput{
		VendorUUID:          vendorUUID,
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	// No purchase_order_approver rows configured for (PORD, PAPV) in this test
	// DB by default, so Approve should report the status doesn't require approval.
	if _, err := Approve(ctx, pool, created.ID, 1); !errors.Is(err, ErrApprovalNotRequired) {
		t.Fatalf("Approve with no configured approvers = %v, want ErrApprovalNotRequired", err)
	}
}

func TestApprove_SignOffFlipsApprovalStatusAndGatesTransition(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreatePurchaseOrderInput{
		VendorUUID:          vendorUUID,
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'PORD'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve PORD record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO purchase_order_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed purchase_order_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM purchase_order_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, papvStatusID)
	})

	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	// Gate: PAPV -> APPV blocked until the sign-off lands.
	if _, err := Transition(ctx, pool, created.ID, "APPV", 1); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Transition PAPV->APPV before approval = %v, want ErrApprovalRequired", err)
	}
	approved, err := Approve(ctx, pool, created.ID, 1)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.ApprovalStatus != "approved" {
		t.Errorf("ApprovalStatus = %q, want approved", approved.ApprovalStatus)
	}
	after, err := Transition(ctx, pool, created.ID, "APPV", 1)
	if err != nil {
		t.Fatalf("Transition PAPV->APPV after approval: %v", err)
	}
	if after.StatusCode != "APPV" {
		t.Errorf("StatusCode = %q, want APPV", after.StatusCode)
	}
}

func TestSearch_ReturnsCreatedPurchaseOrder(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreatePurchaseOrderInput{
		VendorUUID:          vendorUUID,
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	page, err := Search(ctx, pool, "all", "", query.Request{Search: created.Number})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range page.Records {
		if r.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("Search(%q) did not include the created purchase order", created.Number)
	}
}
