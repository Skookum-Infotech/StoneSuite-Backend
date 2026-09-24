//go:build dbtest

package vendorbill

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/database"
	"stonesuite-backend/purchaseorder"
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

	// No approvers configured for this tenant's VBIL/PAPV -> the checkpoint
	// has nothing to wait on, so submitting for approval auto-skips straight
	// to APPV instead of parking the bill on an unresolvable PAPV.
	approved, err := Transition(ctx, pool, bill.ID, "PAPV", 1)
	if err != nil {
		t.Fatalf("Transition to PAPV (auto-skips to APPV): %v", err)
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
	// No approvers configured for this tenant's VBIL/PAPV, so this auto-skips
	// straight to APPV (see TestTransitionAndApprovalGate).
	if _, err := Transition(ctx, pool, bill.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV (auto-skips to APPV): %v", err)
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

// poLineSeed is one order line to seed: what was ordered, how much has been
// received, and its unit price.
type poLineSeed struct{ ordered, received, price float64 }

// seedPurchaseOrder inserts a purchase order directly (CreatePurchaseOrderInput
// embeds an unexported field struct, so its lines cannot be populated from
// here) at the given status, with free-text lines carrying the given received
// quantities. Returns the order's uuid and its lines' internal ids in order.
func seedPurchaseOrder(t *testing.T, pool *pgxpool.Pool, statusCode string, lines []poLineSeed) (poUUID string, lineIDs []int) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var typeID, statusID, vndrTypeID, vndrStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'PORD'`).Scan(&typeID); err != nil {
		t.Fatalf("resolve PORD type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, statusCode).Scan(&statusID); err != nil {
		t.Fatalf("resolve PORD %s status: %v", statusCode, err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VNDR'`).Scan(&vndrTypeID); err != nil {
		t.Fatalf("resolve VNDR type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'ACT_'`,
		vndrTypeID).Scan(&vndrStatusID); err != nil {
		t.Fatalf("resolve vendor ACT_ status: %v", err)
	}
	var vendorID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		VALUES ($1, $2, 'Organization', $3, 1) RETURNING vendor_id`,
		vndrTypeID, vndrStatusID, "Convert Test Vendor "+suffix).Scan(&vendorID); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	var poID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO purchase_order (
			record_type, purchase_order_status, purchase_order_vendor_id, purchase_order_vendor_name, purchase_order_created_by
		) VALUES ($1,$2,$3,$4,1) RETURNING purchase_order_id, purchase_order_uuid::text`,
		typeID, statusID, vendorID, "Convert Test Vendor "+suffix).Scan(&poID, &poUUID); err != nil {
		t.Fatalf("seed purchase order: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE purchase_order SET purchase_order_number = $1 WHERE purchase_order_id = $2`,
		fmt.Sprintf("PORD-%06d", poID), poID); err != nil {
		t.Fatalf("number purchase order: %v", err)
	}
	for i, l := range lines {
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO purchase_order_item (
				purchase_order_id, line_number, item_name, sku, quantity, qty_received, unit_price, item_created_by
			) VALUES ($1,$2,$3,$4,$5,$6,$7,1) RETURNING purchase_order_item_id`,
			poID, i+1, fmt.Sprintf("Line %d %s", i+1, suffix), fmt.Sprintf("SKU-%d-%s", i+1, suffix),
			l.ordered, l.received, l.price).Scan(&id); err != nil {
			t.Fatalf("seed purchase order line %d: %v", i+1, err)
		}
		lineIDs = append(lineIDs, id)
	}
	return poUUID, lineIDs
}

func setReceived(t *testing.T, pool *pgxpool.Pool, lineID int, received float64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE purchase_order_item SET qty_received = $2 WHERE purchase_order_item_id = $1`, lineID, received); err != nil {
		t.Fatalf("set qty_received: %v", err)
	}
}

func billedOn(t *testing.T, pool *pgxpool.Pool, poUUID string) []float64 {
	t.Helper()
	po, err := purchaseorder.Get(context.Background(), pool, poUUID)
	if err != nil {
		t.Fatalf("reload purchase order: %v", err)
	}
	out := make([]float64, len(po.Items))
	for i, l := range po.Items {
		out[i] = l.QtyBilled
	}
	return out
}

// A partly received order bills only what has arrived, at the order's prices;
// lines with nothing received are left off and the bill is numbered 1..n.
func TestConvertFromPurchaseOrder_BillsOnlyWhatWasReceived(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, _ := seedPurchaseOrder(t, pool, "PART", []poLineSeed{{10, 4, 25}, {5, 0, 10}, {8, 8, 2}})

	bill, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("ConvertFromPurchaseOrder: %v", err)
	}
	if bill.StatusCode != "DRFT" {
		t.Errorf("status = %q, want DRFT", bill.StatusCode)
	}
	if bill.PurchaseOrder == nil || bill.PurchaseOrder.ID != poUUID {
		t.Errorf("bill.PurchaseOrder = %+v, want ID %q", bill.PurchaseOrder, poUUID)
	}
	if len(bill.Items) != 2 {
		t.Fatalf("bill lines = %d, want 2 (the line with nothing received is left off)", len(bill.Items))
	}
	if l := bill.Items[0]; l.LineNumber != 1 || l.Quantity != 4 || l.UnitPrice != 25 {
		t.Errorf("line 1 = #%d qty %v @ %v, want #1 qty 4 @ 25 (received, not ordered)", l.LineNumber, l.Quantity, l.UnitPrice)
	}
	if l := bill.Items[1]; l.LineNumber != 2 || l.Quantity != 8 || l.UnitPrice != 2 {
		t.Errorf("line 2 = #%d qty %v @ %v, want #2 qty 8 @ 2 (renumbered, no gap)", l.LineNumber, l.Quantity, l.UnitPrice)
	}
	if want := 4*25.0 + 8*2.0; bill.GrandTotal != want {
		t.Errorf("GrandTotal = %v, want %v", bill.GrandTotal, want)
	}
	if got := billedOn(t, pool, poUUID); got[0] != 4 || got[1] != 0 || got[2] != 8 {
		t.Errorf("qtyBilled per line = %v, want [4 0 8]", got)
	}
}

// Successive deliveries are billed successively: each conversion claims only
// the goods no earlier bill covers, until nothing is left.
func TestConvertFromPurchaseOrder_LaterDeliveryBillsOnlyTheRemainder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, lineIDs := seedPurchaseOrder(t, pool, "PART", []poLineSeed{{10, 4, 25}})

	first, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("first conversion: %v", err)
	}
	if first.Items[0].Quantity != 4 {
		t.Fatalf("first bill qty = %v, want 4", first.Items[0].Quantity)
	}

	// Nothing new has arrived: converting again must not bill the same goods.
	if _, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1); err == nil || !IsClientError(err) ||
		!strings.Contains(err.Error(), "already been billed") {
		t.Fatalf("second conversion with no new delivery = %v, want a 'already been billed' ClientError", err)
	}

	// The rest arrives.
	setReceived(t, pool, lineIDs[0], 10)
	second, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("second conversion after the next delivery: %v", err)
	}
	if second.ID == first.ID {
		t.Error("second conversion returned the first bill, want a new one")
	}
	if len(second.Items) != 1 || second.Items[0].Quantity != 6 {
		t.Fatalf("second bill lines = %+v, want one line of qty 6 (10 received - 4 billed)", second.Items)
	}
	if got := billedOn(t, pool, poUUID); got[0] != 10 {
		t.Errorf("qtyBilled = %v, want [10]", got)
	}

	// Fully received and fully billed: nothing left.
	if _, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1); err == nil || !strings.Contains(err.Error(), "already been billed") {
		t.Fatalf("third conversion = %v, want an 'already been billed' ClientError", err)
	}
}

// A bill that no longer stands -- voided, or deleted -- frees its quantity, so
// the goods can be billed again.
func TestConvertFromPurchaseOrder_VoidedOrDeletedBillFreesItsQuantity(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, _ := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{10, 10, 25}})

	bill, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("first conversion: %v", err)
	}
	if bill.Items[0].Quantity != 10 {
		t.Fatalf("first bill qty = %v, want the full 10", bill.Items[0].Quantity)
	}
	if _, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1); err == nil {
		t.Fatal("converting a fully billed order succeeded, want it refused")
	}

	// Void it.
	if _, err := pool.Exec(ctx, `
		UPDATE vendor_bill SET vendor_bill_status = (
			SELECT rs.record_status_id FROM lkp_record_status rs
			JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
			WHERE rt.record_type_code = 'VBIL' AND rs.record_status_code = 'VOID')
		WHERE vendor_bill_uuid = $1`, bill.ID); err != nil {
		t.Fatalf("void bill: %v", err)
	}
	if got := billedOn(t, pool, poUUID); got[0] != 0 {
		t.Errorf("qtyBilled after voiding the bill = %v, want [0]", got)
	}
	again, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("conversion after the bill was voided: %v", err)
	}
	if again.Items[0].Quantity != 10 {
		t.Errorf("rebilled qty = %v, want 10", again.Items[0].Quantity)
	}

	// Delete that one instead.
	if _, err := pool.Exec(ctx, `UPDATE vendor_bill SET vendor_bill_deleted_at = NOW(), vendor_bill_deleted_by = 1 WHERE vendor_bill_uuid = $1`, again.ID); err != nil {
		t.Fatalf("delete bill: %v", err)
	}
	if got := billedOn(t, pool, poUUID); got[0] != 0 {
		t.Errorf("qtyBilled after deleting the bill = %v, want [0]", got)
	}
}

// Editing a converted draft re-inserts its lines with no PO link (store_update
// .go); the quantity it claimed must still count as billed.
func TestConvertFromPurchaseOrder_EditingTheBillDoesNotReleaseItsQuantity(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, _ := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{10, 10, 25}})
	bill, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("conversion: %v", err)
	}
	// What an edit does to the lines: soft-delete them and re-insert without the link.
	if _, err := pool.Exec(ctx, `
		UPDATE vendor_bill_item SET item_deleted_at = NOW(), purchase_order_item_id = NULL
		WHERE vendor_bill_id = (SELECT vendor_bill_id FROM vendor_bill WHERE vendor_bill_uuid = $1)`, bill.ID); err != nil {
		t.Fatalf("simulate edit: %v", err)
	}
	if got := billedOn(t, pool, poUUID); got[0] != 10 {
		t.Errorf("qtyBilled after the bill was edited = %v, want [10]", got)
	}
	if _, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1); err == nil {
		t.Error("conversion after an edited bill succeeded, want it refused (already billed)")
	}
}

func TestConvertFromPurchaseOrder_RefusesWhatCannotBeBilled(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	cases := []struct {
		name, status string
		lines        []poLineSeed
		want         string
	}{
		{"sent but nothing received", "SENT", []poLineSeed{{10, 0, 25}}, "has received items"},
		{"draft", "DRFT", []poLineSeed{{10, 0, 25}}, "has received items"},
		{"closed with nothing ever received", "CLSD", []poLineSeed{{10, 0, 25}}, "Nothing has been received"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			poUUID, _ := seedPurchaseOrder(t, pool, tt.status, tt.lines)
			_, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
			if err == nil || !IsClientError(err) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want a ClientError mentioning %q", err, tt.want)
			}
		})
	}
}

// A manually created bill can name one of its vendor's purchase orders as a
// plain reference: the link round-trips on Get, but no lines are copied and no
// billed quantity is recorded (only the convert path does that).
func TestCreate_LinksAPurchaseOrderAsAReference(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, _ := seedPurchaseOrder(t, pool, "SENT", []poLineSeed{{10, 0, 25}})
	var vendorUUID string
	if err := pool.QueryRow(ctx, `
		SELECT v.vendor_uuid::text FROM purchase_order po JOIN vendor v ON v.vendor_id = po.purchase_order_vendor_id
		WHERE po.purchase_order_uuid = $1`, poUUID).Scan(&vendorUUID); err != nil {
		t.Fatalf("load the order's vendor: %v", err)
	}

	in := CreateVendorBillInput{
		VendorUUID:        vendorUUID,
		PurchaseOrderUUID: poUUID,
		vendorBillFields: vendorBillFields{
			Items: []LineInput{{LineNumber: 1, Description: "Freight", Quantity: 1, UnitPrice: 40}},
		},
	}
	bill, err := Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if bill.PurchaseOrder == nil || bill.PurchaseOrder.ID != poUUID {
		t.Fatalf("bill.PurchaseOrder = %+v, want ID %q", bill.PurchaseOrder, poUUID)
	}
	if len(bill.Items) != 1 || bill.Items[0].PurchaseOrderItemID != nil {
		t.Errorf("items = %+v, want only the one submitted line with no PO-item lineage", bill.Items)
	}
	if got := billedOn(t, pool, poUUID); got[0] != 0 {
		t.Errorf("qtyBilled = %v, want [0]: a reference link must not count as billing the goods", got)
	}
}

func TestCreate_RefusesAnUnlinkablePurchaseOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, _ := seedPurchaseOrder(t, pool, "SENT", []poLineSeed{{10, 0, 25}})
	otherVendor := seedVendor(t, pool) // not the order's vendor

	cases := []struct{ name, vendor, po string }{
		{"a different vendor's order", otherVendor, poUUID},
		{"an unknown order", otherVendor, "00000000-0000-0000-0000-000000000000"},
		{"a malformed uuid", otherVendor, "not-a-uuid"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			in := CreateVendorBillInput{
				VendorUUID:        tt.vendor,
				PurchaseOrderUUID: tt.po,
				vendorBillFields:  vendorBillFields{Items: []LineInput{{LineNumber: 1, Description: "Freight", Quantity: 1, UnitPrice: 40}}},
			}
			if _, err := Create(ctx, pool, in, 1); err == nil || !IsClientError(err) {
				t.Fatalf("err = %v, want a ClientError (400)", err)
			}
		})
	}
}

func equalQty(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The tenant schema is re-applied on every boot, and it backfills
// vendor_bill_conversion_line for conversions made before per-line tracking
// existed. That backfill must restore ONLY those legacy conversions (full
// ordered quantities -- what the old verbatim copy billed) and must never
// "restore" a line a newer partial conversion deliberately skipped, or every
// restart would inflate what is considered billed. It must also be a no-op the
// second time round.
func TestTenantSchema_BackfillsOnlyLegacyConversions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// A legacy conversion: the whole order billed in full, no line rows recorded.
	legacyPO, _ := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{10, 10, 25}, {5, 5, 10}})
	legacyBill, err := ConvertFromPurchaseOrder(ctx, pool, legacyPO, 1)
	if err != nil {
		t.Fatalf("legacy conversion: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM vendor_bill_conversion_line WHERE vendor_bill_conversion_id = (
			SELECT c.vendor_bill_conversion_id FROM vendor_bill_conversion c
			JOIN vendor_bill vb ON vb.vendor_bill_id = c.vendor_bill_id
			WHERE vb.vendor_bill_uuid = $1)`, legacyBill.ID); err != nil {
		t.Fatalf("simulate a pre-tracking conversion: %v", err)
	}
	if got := billedOn(t, pool, legacyPO); !equalQty(got, []float64{0, 0}) {
		t.Fatalf("legacy order reads as billed %v before the backfill, want [0 0]", got)
	}

	// A newer partial conversion that skipped the line with nothing received.
	partialPO, _ := seedPurchaseOrder(t, pool, "PART", []poLineSeed{{10, 4, 25}, {5, 0, 10}})
	if _, err := ConvertFromPurchaseOrder(ctx, pool, partialPO, 1); err != nil {
		t.Fatalf("partial conversion: %v", err)
	}
	partialBefore := billedOn(t, pool, partialPO)
	if !equalQty(partialBefore, []float64{4, 0}) {
		t.Fatalf("partial order billed %v, want [4 0]", partialBefore)
	}

	for pass := 1; pass <= 2; pass++ {
		if _, err := database.ApplyTenantSchema(ctx, pool); err != nil {
			t.Fatalf("apply tenant schema (pass %d): %v", pass, err)
		}
		if got := billedOn(t, pool, legacyPO); !equalQty(got, []float64{10, 5}) {
			t.Errorf("pass %d: legacy order billed %v, want [10 5] (backfilled at the full ordered quantity)", pass, got)
		}
		if got := billedOn(t, pool, partialPO); !equalQty(got, partialBefore) {
			t.Errorf("pass %d: partial order billed %v, want %v unchanged (its skipped line must not be restored)", pass, got, partialBefore)
		}
	}

	// And the legacy order really is closed to further billing.
	if _, err := ConvertFromPurchaseOrder(ctx, pool, legacyPO, 1); err == nil || !strings.Contains(err.Error(), "already been billed") {
		t.Errorf("converting the fully billed legacy order = %v, want an 'already been billed' error", err)
	}
}

// Create Bill clicked several times at once (a double-click, two tabs) must
// still bill the goods once. The conversion locks the order's lines and only
// THEN reads how much is already billed, so the ones that queue behind the
// first see its committed conversion lines and find nothing left to bill.
func TestConvertFromPurchaseOrder_ConcurrentConversionsBillTheGoodsOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, _ := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{10, 10, 25}, {4, 4, 5}})

	const attempts = 6
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	created, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			created++
		case IsClientError(err) && strings.Contains(err.Error(), "already been billed"):
			refused++
		default:
			t.Errorf("unexpected error from a concurrent conversion: %v", err)
		}
	}
	if created != 1 || refused != attempts-1 {
		t.Errorf("%d bills created and %d refused, want 1 and %d", created, refused, attempts-1)
	}
	if got := billedOn(t, pool, poUUID); !equalQty(got, []float64{10, 4}) {
		t.Errorf("qtyBilled = %v, want [10 4] -- billed exactly once, not %d times", got, created)
	}
}
