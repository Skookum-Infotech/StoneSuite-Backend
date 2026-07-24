// itemreceipt/store_test.go
//go:build dbtest

package itemreceipt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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

// seedSentPO creates a purchase order for `qty` of a fresh catalog item,
// already at SENT — the only state (besides PART) a receipt can be raised
// against.
//
// The order is inserted directly rather than through purchaseorder.Create:
// CreatePurchaseOrderInput embeds an unexported field struct, so its line items
// cannot be populated from outside that package. Direct SQL also keeps this
// helper from breaking when that module's input shape changes.
func seedSentPO(t *testing.T, pool *pgxpool.Pool, qty float64) (poUUID, poLineUUID, itemUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	statusID := func(typeCode, statusCode string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			SELECT rs.record_status_id
			FROM lkp_record_status rs
			JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
			WHERE rt.record_type_code = $1 AND rs.record_status_code = $2`, typeCode, statusCode).Scan(&id); err != nil {
			t.Fatalf("resolve %s/%s status: %v", typeCode, statusCode, err)
		}
		return id
	}
	typeID := func(code string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx,
			`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
			t.Fatalf("resolve %s record type: %v", code, err)
		}
		return id
	}

	var vendorID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		VALUES ($1, $2, 'Organization', $3, 1) RETURNING vendor_id`,
		typeID("VNDR"), statusID("VNDR", "ACT_"), "IR Test Vendor "+suffix).Scan(&vendorID); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}

	var itemID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 1, 25.00, 1) RETURNING inventory_item_id, inventory_item_uuid`,
		"IRSKU-"+suffix, "IR Test Item "+suffix).Scan(&itemID, &itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}

	var poID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO purchase_order (
			record_type, purchase_order_status, purchase_order_vendor_id, purchase_order_vendor_name,
			purchase_order_created_by
		) VALUES ($1,$2,$3,$4,1)
		RETURNING purchase_order_id, purchase_order_uuid`,
		typeID("PORD"), statusID("PORD", "SENT"), vendorID, "IR Test Vendor "+suffix,
	).Scan(&poID, &poUUID); err != nil {
		t.Fatalf("seed purchase order: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE purchase_order SET purchase_order_number = $1 WHERE purchase_order_id = $2`,
		fmt.Sprintf("PORD-%06d", poID), poID); err != nil {
		t.Fatalf("number purchase order: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO purchase_order_item (
			purchase_order_id, line_number, inventory_item_id, item_name, sku,
			unit_id, quantity, unit_price, item_created_by
		) VALUES ($1, 1, $2, $3, $4, 1, $5, 25.00, 1)
		RETURNING purchase_order_item_uuid`,
		poID, itemID, "IR Test Item "+suffix, "IRSKU-"+suffix, qty).Scan(&poLineUUID); err != nil {
		t.Fatalf("seed purchase order item: %v", err)
	}
	return poUUID, poLineUUID, itemUUID
}

// stockFor reads the on-hand quantity and the ledger sum for an item, which
// must always agree (the inventory_ledger invariant).
func stockFor(t *testing.T, pool *pgxpool.Pool, itemUUID string) (onHand, ledgerSum float64) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(s.quantity_on_hand),0), COALESCE((
			SELECT SUM(l.quantity_delta) FROM inventory_ledger l
			JOIN inventory_item i2 ON i2.inventory_item_id = l.inventory_item_id
			WHERE i2.inventory_item_uuid = $1),0)
		FROM inventory_stock s
		JOIN inventory_item i ON i.inventory_item_id = s.inventory_item_id
		WHERE i.inventory_item_uuid = $1`, itemUUID).Scan(&onHand, &ledgerSum); err != nil {
		t.Fatalf("read stock: %v", err)
	}
	return onHand, ledgerSum
}

func poStatus(t *testing.T, pool *pgxpool.Pool, poUUID string) string {
	t.Helper()
	po, err := purchaseorder.Get(context.Background(), pool, poUUID)
	if err != nil {
		t.Fatalf("reload purchase order: %v", err)
	}
	return po.StatusCode
}

func poQtyReceived(t *testing.T, pool *pgxpool.Pool, poUUID string) float64 {
	t.Helper()
	po, err := purchaseorder.Get(context.Background(), pool, poUUID)
	if err != nil {
		t.Fatalf("reload purchase order: %v", err)
	}
	return po.Items[0].QtyReceived
}

// The headline round-trip: partial receipt → PO goes PART, remainder → RCVD,
// void → the PO falls back and stock nets out.
func TestPostAndVoid_RoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID := seedSentPO(t, pool, 100)

	base, _ := stockFor(t, pool, itemUUID)

	// --- first receipt: 40 of 100 -------------------------------------------
	r1, err := Create(ctx, pool, CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 40}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create receipt 1: %v", err)
	}
	if r1.StatusCode != "PEND" {
		t.Errorf("new receipt status = %q, want PEND", r1.StatusCode)
	}
	if r1.Number[:5] != "IRCT-" {
		t.Errorf("Number = %q, want IRCT- prefix", r1.Number)
	}
	// Creating must not move anything yet.
	if onHand, _ := stockFor(t, pool, itemUUID); onHand != base {
		t.Errorf("stock moved on create: %v, want %v", onHand, base)
	}
	if got := poQtyReceived(t, pool, poUUID); got != 0 {
		t.Errorf("qty_received after create = %v, want 0", got)
	}

	r1, err = Post(ctx, pool, r1.ID, PostInput{}, false, 1)
	if err != nil {
		t.Fatalf("Post receipt 1: %v", err)
	}
	if r1.StatusCode != "PART" {
		t.Errorf("receipt 1 status = %q, want PART", r1.StatusCode)
	}
	if got := poQtyReceived(t, pool, poUUID); got != 40 {
		t.Errorf("qty_received after post 1 = %v, want 40", got)
	}
	if got := poStatus(t, pool, poUUID); got != "PART" {
		t.Errorf("PO status after post 1 = %q, want PART", got)
	}
	onHand, ledgerSum := stockFor(t, pool, itemUUID)
	if onHand != base+40 {
		t.Errorf("on-hand after post 1 = %v, want %v", onHand, base+40)
	}
	if onHand != ledgerSum {
		t.Errorf("ledger invariant broken: on-hand %v != ledger sum %v", onHand, ledgerSum)
	}

	// --- second receipt: the remaining 60 -----------------------------------
	r2, err := Create(ctx, pool, CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 60}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create receipt 2: %v", err)
	}
	r2, err = Post(ctx, pool, r2.ID, PostInput{}, false, 1)
	if err != nil {
		t.Fatalf("Post receipt 2: %v", err)
	}
	if r2.StatusCode != "RCVD" {
		t.Errorf("receipt 2 status = %q, want RCVD", r2.StatusCode)
	}
	if got := poStatus(t, pool, poUUID); got != "RCVD" {
		t.Errorf("PO status after post 2 = %q, want RCVD", got)
	}
	if got := poQtyReceived(t, pool, poUUID); got != 100 {
		t.Errorf("qty_received after post 2 = %v, want 100", got)
	}

	// --- void the second receipt --------------------------------------------
	r2, err = Void(ctx, pool, r2.ID, VoidInput{VoidReason: "miscounted at the dock"}, 1)
	if err != nil {
		t.Fatalf("Void receipt 2: %v", err)
	}
	if r2.StatusCode != "VOID" {
		t.Errorf("voided receipt status = %q, want VOID", r2.StatusCode)
	}
	if got := poQtyReceived(t, pool, poUUID); got != 40 {
		t.Errorf("qty_received after void = %v, want 40", got)
	}
	// The PO must fall back — this is the move the user-facing map forbids and
	// only the rollup path may make.
	if got := poStatus(t, pool, poUUID); got != "PART" {
		t.Errorf("PO status after void = %q, want PART", got)
	}
	onHand, ledgerSum = stockFor(t, pool, itemUUID)
	if onHand != base+40 {
		t.Errorf("on-hand after void = %v, want %v", onHand, base+40)
	}
	if onHand != ledgerSum {
		t.Errorf("ledger invariant broken after void: on-hand %v != ledger sum %v", onHand, ledgerSum)
	}
}

// Voiding the only posted receipt must drop the PO all the way back to SENT.
func TestVoid_LastReceiptReturnsOrderToSent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _ := seedSentPO(t, pool, 10)

	r, err := Create(ctx, pool, CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 10}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Post(ctx, pool, r.ID, PostInput{}, false, 1); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if got := poStatus(t, pool, poUUID); got != "RCVD" {
		t.Fatalf("PO status after post = %q, want RCVD", got)
	}
	if _, err := Void(ctx, pool, r.ID, VoidInput{VoidReason: "wrong order"}, 1); err != nil {
		t.Fatalf("Void: %v", err)
	}
	if got := poStatus(t, pool, poUUID); got != "SENT" {
		t.Errorf("PO status after voiding the only receipt = %q, want SENT", got)
	}
}

func TestPost_OverReceiptNeedsApproval(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _ := seedSentPO(t, pool, 100)

	mk := func() string {
		t.Helper()
		r, err := Create(ctx, pool, CreateItemReceiptInput{
			PurchaseOrderUUID: poUUID,
			itemReceiptFields: itemReceiptFields{
				Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 150}},
			},
		}, 1)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		return r.ID
	}

	// Without the grant: refused.
	if _, err := Post(ctx, pool, mk(), PostInput{}, false, 1); !errors.Is(err, ErrOverReceipt) {
		t.Fatalf("Post over tolerance without grant = %v, want ErrOverReceipt", err)
	}
	if got := poQtyReceived(t, pool, poUUID); got != 0 {
		t.Errorf("refused post still moved qty_received to %v", got)
	}

	// With the grant but no reason: client error.
	if _, err := Post(ctx, pool, mk(), PostInput{}, true, 1); !IsClientError(err) {
		t.Fatalf("Post over tolerance with grant and no reason = %v, want ClientError", err)
	}

	// With the grant and a reason: accepted, and it lands past the ordered qty.
	got, err := Post(ctx, pool, mk(), PostInput{OverReceiptReason: "vendor shipped a full pallet"}, true, 1)
	if err != nil {
		t.Fatalf("Post over tolerance with grant and reason: %v", err)
	}
	if got.StatusCode != "RCVD" {
		t.Errorf("over-receipt status = %q, want RCVD", got.StatusCode)
	}
	if q := poQtyReceived(t, pool, poUUID); q != 150 {
		t.Errorf("qty_received after over-receipt = %v, want 150 (the relaxed CHECK must permit it)", q)
	}
}

// A small over-delivery inside the tolerance must not need any override.
func TestPost_WithinTolerancePostsSilently(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _ := seedSentPO(t, pool, 100)

	r, err := Create(ctx, pool, CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 103}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Post(ctx, pool, r.ID, PostInput{}, false, 1); err != nil {
		t.Fatalf("Post within tolerance: %v", err)
	}
	if q := poQtyReceived(t, pool, poUUID); q != 103 {
		t.Errorf("qty_received = %v, want 103", q)
	}
}

// Rejected goods are recorded but neither enter stock nor satisfy the order.
func TestPost_RejectedQuantityNeitherStocksNorSettles(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID := seedSentPO(t, pool, 100)
	base, _ := stockFor(t, pool, itemUUID)

	r, err := Create(ctx, pool, CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 50, QtyRejected: 20}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Post(ctx, pool, r.ID, PostInput{}, false, 1); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if q := poQtyReceived(t, pool, poUUID); q != 30 {
		t.Errorf("qty_received = %v, want 30 (50 received less 20 rejected)", q)
	}
	if onHand, _ := stockFor(t, pool, itemUUID); onHand != base+30 {
		t.Errorf("on-hand = %v, want %v", onHand, base+30)
	}
}

func TestPost_RefusesDoublePost(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _ := seedSentPO(t, pool, 100)

	r, err := Create(ctx, pool, CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 10}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Post(ctx, pool, r.ID, PostInput{}, false, 1); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if _, err := Post(ctx, pool, r.ID, PostInput{}, false, 1); !errors.Is(err, ErrAlreadyPosted) {
		t.Fatalf("second Post = %v, want ErrAlreadyPosted", err)
	}
	if q := poQtyReceived(t, pool, poUUID); q != 10 {
		t.Errorf("qty_received after refused double post = %v, want 10", q)
	}
}

func TestUpdateAndDelete_PendingOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _ := seedSentPO(t, pool, 100)

	r, err := Create(ctx, pool, CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			PackingSlip: "PS-1",
			Items:       []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 10}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Pending: editable.
	updated, err := Update(ctx, pool, r.ID, UpdateItemReceiptInput{
		itemReceiptFields: itemReceiptFields{
			PackingSlip: "PS-2",
			Items:       []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 25}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Update at PEND: %v", err)
	}
	if updated.PackingSlip != "PS-2" || updated.Items[0].QtyReceived != 25 {
		t.Errorf("update did not take: %+v", updated.Items)
	}
	if len(updated.Items) != 1 {
		t.Errorf("line replacement left %d lines, want 1", len(updated.Items))
	}

	// Posted: immutable, and undeletable.
	if _, err := Post(ctx, pool, r.ID, PostInput{}, false, 1); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if _, err := Update(ctx, pool, r.ID, UpdateItemReceiptInput{
		itemReceiptFields: itemReceiptFields{Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 5}}},
	}, 1); !errors.Is(err, ErrAlreadyPosted) {
		t.Fatalf("Update after post = %v, want ErrAlreadyPosted", err)
	}
	if err := SoftDelete(ctx, pool, r.ID, 1); !errors.Is(err, ErrAlreadyPosted) {
		t.Fatalf("SoftDelete after post = %v, want ErrAlreadyPosted", err)
	}

	// Voided: deletable again.
	if _, err := Void(ctx, pool, r.ID, VoidInput{VoidReason: "test"}, 1); err != nil {
		t.Fatalf("Void: %v", err)
	}
	if err := SoftDelete(ctx, pool, r.ID, 1); err != nil {
		t.Fatalf("SoftDelete after void: %v", err)
	}
	if _, err := Get(ctx, pool, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestCreate_RejectsUnreceivableOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _ := seedSentPO(t, pool, 10)

	// Short-close the order; it can no longer receive.
	if _, err := purchaseorder.Transition(ctx, pool, poUUID, "CLSD", 1); err != nil {
		t.Fatalf("close purchase order: %v", err)
	}
	_, err := Create(ctx, pool, CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 1}},
		},
	}, 1)
	if !errors.Is(err, ErrPONotReceivable) {
		t.Fatalf("Create against a closed order = %v, want ErrPONotReceivable", err)
	}
}

func TestCreate_RejectsForeignOrderLine(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poA, _, _ := seedSentPO(t, pool, 10)
	_, lineB, _ := seedSentPO(t, pool, 10)

	// Line B belongs to a different order — it must not be splice-able onto a
	// receipt for order A.
	_, err := Create(ctx, pool, CreateItemReceiptInput{
		PurchaseOrderUUID: poA,
		itemReceiptFields: itemReceiptFields{
			Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: lineB, QtyReceived: 1}},
		},
	}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with a foreign order line = %v, want ClientError", err)
	}
}

func TestCreate_RejectsDuplicateOrderLine(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _ := seedSentPO(t, pool, 100)

	_, err := Create(ctx, pool, CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			Items: []LineInput{
				{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 10},
				{LineNumber: 2, PurchaseOrderItemUUID: poLineUUID, QtyReceived: 10},
			},
		},
	}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with a duplicated order line = %v, want ClientError", err)
	}
}
