// itemreceipt/store_slabs_test.go — receiving slab-tracked items slab by slab.
//go:build dbtest

package itemreceipt

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/inventory"
	"stonesuite-backend/purchaseorder"
)

const (
	tenByFiveFeetSqft = 50.0 // 3048 x 1524 mm
	slabLengthMM      = 3048.0
	slabWidthMM       = 1524.0
	slabThicknessMM   = 30.0
)

// slabPO seeds a SENT purchase order for `orderedSqft` of a fresh catalogue item
// that is slab-tracked in square feet, and returns the ids the tests need.
func slabPO(t *testing.T, pool *pgxpool.Pool, orderedSqft float64) (poUUID, poLineUUID, itemUUID, poNumber string) {
	t.Helper()
	poUUID, poLineUUID, itemUUID = seedSentPO(t, pool, orderedSqft)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		UPDATE inventory_item
		SET inventory_item_unit_id = (SELECT unit_id FROM lkp_unit WHERE unit_code = 'SQFT'),
		    inventory_item_tracking = 'serialized'
		WHERE inventory_item_uuid = $1`, itemUUID); err != nil {
		t.Fatalf("make item slab-tracked: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT purchase_order_number FROM purchase_order WHERE purchase_order_uuid = $1`, poUUID).Scan(&poNumber); err != nil {
		t.Fatalf("read PO number: %v", err)
	}
	return poUUID, poLineUUID, itemUUID, poNumber
}

func defaultWarehouse(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(), `
		SELECT company_location_id FROM company_location WHERE is_default AND deleted_at IS NULL`).Scan(&id); err != nil {
		t.Fatalf("read default location: %v", err)
	}
	return id
}

// seedBin creates a bin in the given warehouse and returns its uuid.
func seedBin(t *testing.T, pool *pgxpool.Pool, warehouseID int) string {
	t.Helper()
	code := fmt.Sprintf("IRB-%d", time.Now().UnixNano()%1_000_000_000)
	var uuid string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_bin (warehouse_id, bin_code, bin_path) VALUES ($1, $2, $2)
		RETURNING inventory_bin_uuid`, warehouseID, code).Scan(&uuid); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	return uuid
}

// slabTotals reads an item's on-hand stock alongside what each ledger says.
// For a slab-tracked item the bulk ledger must stay at zero and the slab ledger
// must equal on-hand.
func slabTotals(t *testing.T, pool *pgxpool.Pool, itemUUID string) (onHand, slabLedger, bulkLedger float64) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT
		  COALESCE((SELECT SUM(s.quantity_on_hand) FROM inventory_stock s
		            JOIN inventory_item i ON i.inventory_item_id = s.inventory_item_id
		            WHERE i.inventory_item_uuid = $1), 0),
		  COALESCE((SELECT SUM(l.quantity_delta) FROM inventory_slab_ledger l
		            JOIN inventory_item i ON i.inventory_item_id = l.inventory_item_id
		            WHERE i.inventory_item_uuid = $1), 0),
		  COALESCE((SELECT SUM(l.quantity_delta) FROM inventory_ledger l
		            JOIN inventory_item i ON i.inventory_item_id = l.inventory_item_id
		            WHERE i.inventory_item_uuid = $1), 0)`, itemUUID).Scan(&onHand, &slabLedger, &bulkLedger); err != nil {
		t.Fatalf("read slab totals: %v", err)
	}
	return onHand, slabLedger, bulkLedger
}

func slabStatuses(t *testing.T, pool *pgxpool.Pool, itemUUID string) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT s.slab_serial, s.slab_status FROM inventory_slab s
		JOIN inventory_item i ON i.inventory_item_id = s.inventory_item_id
		WHERE i.inventory_item_uuid = $1`, itemUUID)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var serial, status string
		require.NoError(t, rows.Scan(&serial, &status))
		out[serial] = status
	}
	require.NoError(t, rows.Err())
	return out
}

func slabReceipt(poUUID, poLineUUID string, warehouseID int, slabs ...SlabInput) CreateItemReceiptInput {
	return CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			WarehouseID: &warehouseID,
			Items:       []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: poLineUUID, Slabs: slabs}},
		},
	}
}

func fullSlab() SlabInput {
	return SlabInput{LengthMM: slabLengthMM, WidthMM: slabWidthMM, ThicknessMM: slabThicknessMM}
}

func TestSlabReceipt_PostCreatesSlabsAndStock(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID, poNumber := slabPO(t, pool, 500)
	wh := defaultWarehouse(t, pool)
	bin := seedBin(t, pool, wh)

	binned := fullSlab()
	binned.BinID, binned.Lot, binned.Grade = bin, "LOT-9", "A"

	ir, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, fullSlab(), binned), false, 1)
	require.NoError(t, err)
	assert.Equal(t, "PART", ir.StatusCode, "100 of 500 ordered is a partial receipt")

	require.Len(t, ir.Items, 1)
	line := ir.Items[0]
	assert.InDelta(t, 2*tenByFiveFeetSqft, line.QtyReceived, 1e-9, "qty is the sum of the slab areas, not typed")
	assert.Zero(t, line.QtyRejected)
	require.Len(t, line.Slabs, 2)

	assert.Equal(t, poNumber+"-001", line.Slabs[0].Serial)
	assert.Equal(t, poNumber+"-002", line.Slabs[1].Serial)
	for i, s := range line.Slabs {
		assert.InDelta(t, tenByFiveFeetSqft, s.Area, 1e-9, "slab %d", i+1)
		assert.Equal(t, "available", s.UnitStatus)
		require.NotNil(t, s.UnitID, "a posted slab links to its inventory unit")
	}
	assert.Nil(t, line.Slabs[0].BinID)
	require.NotNil(t, line.Slabs[1].BinID)
	assert.Equal(t, bin, *line.Slabs[1].BinID)

	// Stock arrives through the slab ledger only — the bulk ledger must not also
	// count it, which is the double count this design removes.
	onHand, slabLedger, bulkLedger := slabTotals(t, pool, itemUUID)
	assert.InDelta(t, 2*tenByFiveFeetSqft, onHand, 1e-9)
	assert.InDelta(t, onHand, slabLedger, 1e-9, "slab ledger must equal on-hand")
	assert.Zero(t, bulkLedger, "a slab-tracked item never touches the bulk ledger")
	assert.InDelta(t, 2*tenByFiveFeetSqft, poQtyReceived(t, pool, poUUID), 1e-9)

	// Each unit knows which receipt brought it in, and carries the PO's vendor.
	unit, err := inventory.GetUnit(ctx, pool, *line.Slabs[1].UnitID)
	require.NoError(t, err)
	assert.Equal(t, ir.Number, unit.ReceiptNumber)
	require.NotNil(t, unit.ReceiptID)
	assert.Equal(t, ir.ID, *unit.ReceiptID)
	assert.Equal(t, wh, unit.WarehouseID)
	assert.Equal(t, "LOT-9", unit.Lot)
	assert.Equal(t, "A", unit.Grade)
	assert.NotNil(t, unit.VendorID, "the vendor comes from the purchase order")
	assert.InDelta(t, tenByFiveFeetSqft, unit.Area, 1e-9)

	// A second receipt against the same order carries the numbering on.
	ir2, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, fullSlab()), false, 1)
	require.NoError(t, err)
	require.Len(t, ir2.Items[0].Slabs, 1)
	assert.Equal(t, poNumber+"-003", ir2.Items[0].Slabs[0].Serial)
}

func TestSlabReceipt_PendingReceiptCreatesNothingUntilPosted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID, poNumber := slabPO(t, pool, 500)
	wh := defaultWarehouse(t, pool)

	ir, err := Create(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, fullSlab()), 1)
	require.NoError(t, err)
	assert.Equal(t, "PEND", ir.StatusCode)
	require.Len(t, ir.Items[0].Slabs, 1)
	assert.Empty(t, ir.Items[0].Slabs[0].Serial, "the serial is assigned at post, not at save")
	assert.Nil(t, ir.Items[0].Slabs[0].UnitID)
	assert.Empty(t, slabStatuses(t, pool, itemUUID), "no inventory unit exists before posting")
	onHand, _, _ := slabTotals(t, pool, itemUUID)
	assert.Zero(t, onHand)

	// Editing a pending receipt replaces its slabs.
	two := slabReceipt(poUUID, poLineUUID, wh, fullSlab(), fullSlab())
	ir, err = Update(ctx, pool, ir.ID, UpdateItemReceiptInput{itemReceiptFields: two.itemReceiptFields}, 1)
	require.NoError(t, err)
	require.Len(t, ir.Items[0].Slabs, 2)
	assert.InDelta(t, 2*tenByFiveFeetSqft, ir.Items[0].QtyReceived, 1e-9)

	ir, err = Post(ctx, pool, ir.ID, PostInput{}, false, 1)
	require.NoError(t, err)
	assert.Equal(t, poNumber+"-001", ir.Items[0].Slabs[0].Serial)
	assert.Equal(t, poNumber+"-002", ir.Items[0].Slabs[1].Serial)
	assert.Len(t, slabStatuses(t, pool, itemUUID), 2)
}

func TestSlabReceipt_VoidTakesTheSlabsBackOut(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID, poNumber := slabPO(t, pool, 500)
	wh := defaultWarehouse(t, pool)

	ir, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, fullSlab(), fullSlab()), false, 1)
	require.NoError(t, err)

	ir, err = Void(ctx, pool, ir.ID, VoidInput{VoidReason: "wrong shipment"}, 1)
	require.NoError(t, err)
	assert.Equal(t, "VOID", ir.StatusCode)
	assert.Equal(t, "SENT", poStatus(t, pool, poUUID), "nothing received any more")
	assert.Zero(t, poQtyReceived(t, pool, poUUID))

	statuses := slabStatuses(t, pool, itemUUID)
	assert.Equal(t, map[string]string{poNumber + "-001": "scrapped", poNumber + "-002": "scrapped"}, statuses)
	onHand, slabLedger, bulkLedger := slabTotals(t, pool, itemUUID)
	assert.Zero(t, onHand)
	assert.Zero(t, slabLedger)
	assert.Zero(t, bulkLedger)

	// A voided receipt's serials are never handed out again.
	next, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, fullSlab()), false, 1)
	require.NoError(t, err)
	assert.Equal(t, poNumber+"-003", next.Items[0].Slabs[0].Serial)
}

func TestSlabReceipt_VoidRefusedOnceASlabHasMovedOn(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID, poNumber := slabPO(t, pool, 500)
	wh := defaultWarehouse(t, pool)

	ir, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, fullSlab(), fullSlab()), false, 1)
	require.NoError(t, err)

	// One slab is written off after receipt.
	require.NoError(t, inventory.ScrapUnit(ctx, pool, *ir.Items[0].Slabs[0].UnitID, nil, "cracked", 1))

	_, err = Void(ctx, pool, ir.ID, VoidInput{VoidReason: "changed my mind"}, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "must be a 400, got %T: %v", err, err)
	assert.Contains(t, err.Error(), poNumber+"-001", "the offending serial is named")
	assert.Contains(t, err.Error(), "scrapped")

	// All or nothing: the untouched slab is still in stock and the receipt stands.
	got, err := Get(ctx, pool, ir.ID)
	require.NoError(t, err)
	assert.NotEqual(t, "VOID", got.StatusCode)
	assert.Equal(t, "available", slabStatuses(t, pool, itemUUID)[poNumber+"-002"])
	assert.InDelta(t, 2*tenByFiveFeetSqft, poQtyReceived(t, pool, poUUID), 1e-9,
		"the order still counts the receipt")
}

func TestSlabReceipt_Validation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _, _ := slabPO(t, pool, 500)
	wh := defaultWarehouse(t, pool)

	noWarehouse := slabReceipt(poUUID, poLineUUID, wh, fullSlab())
	noWarehouse.WarehouseID = nil

	rejected := slabReceipt(poUUID, poLineUUID, wh, fullSlab())
	rejected.Items[0].QtyRejected = 1

	unknownBin := fullSlab()
	unknownBin.BinID = "00000000-0000-0000-0000-000000000000"

	tests := []struct {
		name    string
		in      CreateItemReceiptInput
		wantMsg string
	}{
		{"no slabs on a slab line", slabReceipt(poUUID, poLineUUID, wh), "at least one slab"},
		{"no location named", noWarehouse, "location is required"},
		{"rejected quantity on a slab line", rejected, "no rejected quantity"},
		{"unknown bin", slabReceipt(poUUID, poLineUUID, wh, unknownBin), "Line 1, slab 1"},
		{"bad dimensions", slabReceipt(poUUID, poLineUUID, wh, SlabInput{LengthMM: 0, WidthMM: 1, ThicknessMM: 1}), "greater than zero"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CreateAndPost(ctx, pool, tc.in, false, 1)
			require.Error(t, err)
			assert.True(t, IsClientError(err), "must be a 400, got %T: %v", err, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestSlabReceipt_SlabsRefusedOnAQuantityItem(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _ := seedSentPO(t, pool, 10) // default tracking: quantity
	wh := defaultWarehouse(t, pool)

	in := slabReceipt(poUUID, poLineUUID, wh, fullSlab())
	in.Items[0].QtyReceived = 10
	_, err := CreateAndPost(ctx, pool, in, false, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
	assert.Contains(t, err.Error(), "slab-tracked")
}

func TestNextSlabSequence(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _, poNumber := slabPO(t, pool, 500)
	wh := defaultWarehouse(t, pool)

	seq, err := NextSlabSequence(ctx, pool, poUUID)
	require.NoError(t, err)
	assert.Equal(t, SlabSequence{Prefix: poNumber + "-", Next: 1}, *seq)

	_, err = CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, fullSlab(), fullSlab()), false, 1)
	require.NoError(t, err)

	seq, err = NextSlabSequence(ctx, pool, poUUID)
	require.NoError(t, err)
	assert.Equal(t, 3, seq.Next, "the preview continues where the last receipt stopped")

	_, err = NextSlabSequence(ctx, pool, "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
	assert.True(t, IsClientError(err))
}

// The order line shows how many slabs have arrived against it, next to the
// count the buyer expected, so "11 of about 12" is visible from the order.
func TestSlabReceipt_OrderLineCountsSlabsReceived(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _, _ := slabPO(t, pool, 500)
	wh := defaultWarehouse(t, pool)
	_, err := pool.Exec(ctx, `UPDATE purchase_order_item SET expected_slabs = 12 WHERE purchase_order_item_uuid = $1`, poLineUUID)
	require.NoError(t, err)

	line := func() purchaseorder.Line {
		t.Helper()
		po, err := purchaseorder.Get(ctx, pool, poUUID)
		require.NoError(t, err)
		require.Len(t, po.Items, 1)
		return po.Items[0]
	}

	start := line()
	require.NotNil(t, start.ExpectedSlabs)
	assert.Equal(t, 12, *start.ExpectedSlabs)
	assert.Zero(t, start.SlabsReceived)

	// A receipt that has not posted has brought nothing in yet.
	_, err = Create(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, fullSlab(), fullSlab()), 1)
	require.NoError(t, err)
	assert.Zero(t, line().SlabsReceived)

	first, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, fullSlab(), fullSlab()), false, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, line().SlabsReceived)

	_, err = CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, fullSlab()), false, 1)
	require.NoError(t, err)
	assert.Equal(t, 3, line().SlabsReceived, "receipts add up across shipments")

	// Voiding a receipt takes its slabs back out of the count.
	_, err = Void(ctx, pool, first.ID, VoidInput{VoidReason: "wrong bundle"}, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, line().SlabsReceived)

	end := line()
	require.NotNil(t, end.ExpectedSlabs)
	assert.Equal(t, 12, *end.ExpectedSlabs, "the expectation is untouched by receiving")
}
