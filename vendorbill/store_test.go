//go:build dbtest

package vendorbill

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

// seedVendor inserts a minimal live vendor and returns its uuid.
func seedVendor(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var typeID, statusID, uuid string
	if err := pool.QueryRow(ctx, `SELECT record_type_id::text FROM lkp_record_type WHERE record_type_code = 'VNDR'`).Scan(&typeID); err != nil {
		t.Fatalf("resolve VNDR type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id::text FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'ACT_'`, typeID).Scan(&statusID); err != nil {
		t.Fatalf("resolve vendor ACT_ status: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		VALUES ($1, $2, 'Organization', 'Test Vendor Inc', 1)
		RETURNING vendor_uuid::text`, typeID, statusID).Scan(&uuid); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	return uuid
}

func TestCreateAndGet(t *testing.T) {
	pool := testPool(t)
	vendorUUID := seedVendor(t, pool)

	in := CreateVendorBillInput{
		VendorUUID: vendorUUID,
		vendorBillFields: vendorBillFields{
			VendorInvoiceNumber: "VND-INV-001",
			BillDate:            "2026-08-01",
			SalesTaxPercent:     8.25,
			Items: []LineInput{
				{LineNumber: 1, Description: "Consulting hours", Quantity: 10, UnitPrice: 100},
			},
		},
	}
	bill, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if bill.StatusCode != "DRFT" {
		t.Errorf("new bill status = %q, want DRFT", bill.StatusCode)
	}
	if bill.GrandTotal != 1082.50 {
		t.Errorf("GrandTotal = %v, want 1082.50", bill.GrandTotal)
	}
	if len(bill.Items) != 1 {
		t.Fatalf("Items len = %d, want 1", len(bill.Items))
	}

	got, err := Get(context.Background(), pool, bill.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Number != bill.Number {
		t.Errorf("Get().Number = %q, want %q", got.Number, bill.Number)
	}
}

func TestUpdateReplacesLines(t *testing.T) {
	pool := testPool(t)
	vendorUUID := seedVendor(t, pool)
	ctx := context.Background()

	bill, err := Create(ctx, pool, CreateVendorBillInput{
		VendorUUID: vendorUUID,
		vendorBillFields: vendorBillFields{
			BillDate: "2026-08-01",
			Items:    []LineInput{{LineNumber: 1, Description: "Line A", Quantity: 1, UnitPrice: 50}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := Update(ctx, pool, bill.ID, UpdateVendorBillInput{
		vendorBillFields: vendorBillFields{
			BillDate: "2026-08-01",
			Items:    []LineInput{{LineNumber: 1, Description: "Line B", Quantity: 2, UnitPrice: 75}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.Items) != 1 || updated.Items[0].Description != "Line B" {
		t.Fatalf("Update did not replace lines: %+v", updated.Items)
	}
	if updated.GrandTotal != 150 {
		t.Errorf("GrandTotal = %v, want 150", updated.GrandTotal)
	}
}

func TestTransitionAndApprovalGate(t *testing.T) {
	pool := testPool(t)
	vendorUUID := seedVendor(t, pool)
	ctx := context.Background()

	bill, err := Create(ctx, pool, CreateVendorBillInput{
		VendorUUID: vendorUUID,
		vendorBillFields: vendorBillFields{
			BillDate: "2026-08-01",
			Items:    []LineInput{{LineNumber: 1, Description: "Line A", Quantity: 1, UnitPrice: 100}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	moved, err := Transition(ctx, pool, bill.ID, "PAPV", 1)
	if err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	if moved.StatusCode != "PAPV" {
		t.Fatalf("status = %q, want PAPV", moved.StatusCode)
	}

	// No approvers configured for this tenant's VBIL/PAPV -> gate is open,
	// so APPV should succeed with no Approve call first.
	approved, err := Transition(ctx, pool, bill.ID, "APPV", 1)
	if err != nil {
		t.Fatalf("Transition to APPV with no configured approvers: %v", err)
	}
	if approved.StatusCode != "APPV" {
		t.Fatalf("status = %q, want APPV", approved.StatusCode)
	}

	if _, err := Transition(ctx, pool, bill.ID, "DRFT", 1); err != ErrInvalidTransition {
		t.Errorf("Transition APPV->DRFT = %v, want ErrInvalidTransition", err)
	}
}

func TestRecordPaymentDerivesStatus(t *testing.T) {
	pool := testPool(t)
	vendorUUID := seedVendor(t, pool)
	ctx := context.Background()

	bill, err := Create(ctx, pool, CreateVendorBillInput{
		VendorUUID: vendorUUID,
		vendorBillFields: vendorBillFields{
			BillDate: "2026-08-01",
			Items:    []LineInput{{LineNumber: 1, Description: "Line A", Quantity: 1, UnitPrice: 100}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, bill.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	if _, err := Transition(ctx, pool, bill.ID, "APPV", 1); err != nil {
		t.Fatalf("Transition to APPV: %v", err)
	}

	partial, err := RecordPayment(ctx, pool, bill.ID, RecordPaymentInput{Amount: 40, PaidAt: "2026-08-05"}, 1)
	if err != nil {
		t.Fatalf("RecordPayment (partial): %v", err)
	}
	if partial.StatusCode != "PART" {
		t.Errorf("status after partial payment = %q, want PART", partial.StatusCode)
	}
	if partial.BalanceDue != 60 {
		t.Errorf("BalanceDue = %v, want 60", partial.BalanceDue)
	}

	_, err = RecordPayment(ctx, pool, bill.ID, RecordPaymentInput{Amount: 1000, PaidAt: "2026-08-06"}, 1)
	if err == nil {
		t.Fatal("RecordPayment should reject an amount exceeding the outstanding balance")
	}
	if !IsClientError(err) {
		t.Errorf("overpay error = %v, want ClientError", err)
	}

	full, err := RecordPayment(ctx, pool, bill.ID, RecordPaymentInput{Amount: 60, PaidAt: "2026-08-07"}, 1)
	if err != nil {
		t.Fatalf("RecordPayment (final): %v", err)
	}
	if full.StatusCode != "PAID" {
		t.Errorf("status after full payment = %q, want PAID", full.StatusCode)
	}
	if full.BalanceDue != 0 {
		t.Errorf("BalanceDue = %v, want 0", full.BalanceDue)
	}
}

func TestConvertFromPurchaseOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var poUUID string
	err := pool.QueryRow(ctx, `
		SELECT po.purchase_order_uuid::text FROM purchase_order po
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE rs.record_status_code IN ('RCVD','CLSD') AND po.purchase_order_deleted_at IS NULL
		LIMIT 1`).Scan(&poUUID)
	if err != nil {
		t.Skip("no RCVD/CLSD purchase order fixture available; seed one to exercise this path")
	}

	bill, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("ConvertFromPurchaseOrder: %v", err)
	}
	if bill.PurchaseOrder == nil || bill.PurchaseOrder.ID != poUUID {
		t.Errorf("converted bill.PurchaseOrder = %+v, want ID %q", bill.PurchaseOrder, poUUID)
	}
	if len(bill.Items) == 0 {
		t.Error("converted bill has no lines")
	}

	// A second conversion of the same PO must succeed and create a DIFFERENT
	// bill (AD-8: installment billing, no idempotent short-circuit).
	second, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("second ConvertFromPurchaseOrder: %v", err)
	}
	if second.ID == bill.ID {
		t.Error("second conversion returned the same bill id; expected a new bill per AD-8")
	}
}
