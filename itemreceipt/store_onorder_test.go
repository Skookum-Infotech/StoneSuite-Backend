// itemreceipt/store_onorder_test.go — receiving the slabs a purchase order already
// put on order: ticking a slab on the receipt flips that slab to available.
//go:build dbtest

package itemreceipt

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/inventory"
)

// putOnOrder does what sending the order does: it mints n On Order slabs for the
// line (the real minting lives in package purchaseorder, which this package's
// seed helper bypasses by inserting the order already Sent) and returns their
// uuids in serial order.
func putOnOrder(t *testing.T, pool *pgxpool.Pool, poLineUUID string, n int, thicknessMM float64) []string {
	t.Helper()
	ctx := context.Background()

	var poItemID, vendorID, itemID int
	var poNumber string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT poi.purchase_order_item_id, po.purchase_order_vendor_id, poi.inventory_item_id,
		       po.purchase_order_number
		FROM purchase_order_item poi JOIN purchase_order po ON po.purchase_order_id = poi.purchase_order_id
		WHERE poi.purchase_order_item_uuid = $1`, poLineUUID).Scan(&poItemID, &vendorID, &itemID, &poNumber))

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	item, err := inventory.ResolveItemUnitByID(ctx, tx, itemID)
	require.NoError(t, err)
	prefix, err := inventory.SlabSerialPrefix(poNumber)
	require.NoError(t, err)
	next, err := inventory.NextSlabNumber(ctx, tx, prefix)
	require.NoError(t, err)
	_, err = inventory.CreateOnOrderUnitsTx(ctx, tx, inventory.OnOrderParams{
		PurchaseOrderItemID: poItemID, PONumber: poNumber, Item: item,
		WarehouseID: defaultWarehouse(t, pool), VendorID: &vendorID,
		LengthMM: slabLengthMM, WidthMM: slabWidthMM, ThicknessMM: thicknessMM,
		Count: n, SerialPrefix: prefix, FirstNumber: next,
	}, 1)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	rows, err := pool.Query(ctx, `
		SELECT inventory_slab_uuid::text FROM inventory_slab
		WHERE purchase_order_item_id = $1 ORDER BY slab_serial`, poItemID)
	require.NoError(t, err)
	defer rows.Close()
	var uuids []string
	for rows.Next() {
		var u string
		require.NoError(t, rows.Scan(&u))
		uuids = append(uuids, u)
	}
	require.NoError(t, rows.Err())
	return uuids
}

func ticked(uuid string) SlabInput { return SlabInput{OnOrderSlabID: uuid} }

func requireClientError(t *testing.T, err error, contains string) {
	t.Helper()
	require.Error(t, err)
	var ce ClientError
	require.True(t, errors.As(err, &ce), "must be a 400, not a 500: %v", err)
	assert.Contains(t, err.Error(), contains)
}

func inspectionStatus(t *testing.T, pool *pgxpool.Pool, slabUUID string) string {
	t.Helper()
	var s string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT inspection_status FROM inventory_slab WHERE inventory_slab_uuid::text = $1`, slabUUID).Scan(&s))
	return s
}

func TestOnOrderReceipt_OnlyTickedSlabsBecomeAvailable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID, poNumber := slabPO(t, pool, 3*tenByFiveFeetSqft)
	wh := defaultWarehouse(t, pool)
	bin := seedBin(t, pool, wh)
	slabs := putOnOrder(t, pool, poLineUUID, 3, slabThicknessMM)

	onHand, _, _ := slabTotals(t, pool, itemUUID)
	require.Zero(t, onHand, "slabs on order are not stock")

	withBin := ticked(slabs[1])
	withBin.BinID = bin
	ir, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[0]), withBin), false, 1)
	require.NoError(t, err)
	assert.Equal(t, "PART", ir.StatusCode, "2 of the 3 ordered slabs arrived")

	line := ir.Items[0]
	assert.InDelta(t, 2*tenByFiveFeetSqft, line.QtyReceived, 1e-9, "qty is the area of the ticked slabs")
	require.Len(t, line.Slabs, 2)
	for i, s := range line.Slabs {
		assert.Equal(t, poNumber+"-00"+string(rune('1'+i)), s.Serial, "a ticked slab keeps the serial it was ordered with")
		assert.Equal(t, "available", s.UnitStatus)
		require.NotNil(t, s.OnOrderSlabID)
		assert.Equal(t, slabs[i], *s.OnOrderSlabID)
		require.NotNil(t, s.UnitID)
		assert.Equal(t, slabs[i], *s.UnitID, "it is the SAME inventory unit, not a new one")
	}
	assert.Nil(t, line.Slabs[0].BinID)
	require.NotNil(t, line.Slabs[1].BinID)
	assert.Equal(t, bin, *line.Slabs[1].BinID)

	assert.Equal(t, map[string]string{
		poNumber + "-001": "available",
		poNumber + "-002": "available",
		poNumber + "-003": "on_order",
	}, slabStatuses(t, pool, itemUUID), "the slab nobody ticked is still on order")

	onHand, slabLedger, bulkLedger := slabTotals(t, pool, itemUUID)
	assert.InDelta(t, 2*tenByFiveFeetSqft, onHand, 1e-9, "only the arrived slabs are on hand")
	assert.InDelta(t, onHand, slabLedger, 1e-9)
	assert.Zero(t, bulkLedger)
	assert.Equal(t, "pending", inspectionStatus(t, pool, slabs[0]), "an arrived slab awaits inspection, like any received slab")
	assert.Equal(t, "PART", poStatus(t, pool, poUUID))

	unit, err := inventory.GetUnit(ctx, pool, slabs[1])
	require.NoError(t, err)
	assert.Equal(t, wh, unit.WarehouseID)
	assert.Equal(t, ir.Number, unit.ReceiptNumber, "the unit knows which receipt brought it in")
	assert.Equal(t, "available", unit.Status)

	// The next receipt finds only what is left, and completes the order.
	ir2, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[2])), false, 1)
	require.NoError(t, err)
	assert.Equal(t, "RCVD", ir2.StatusCode)
	assert.Equal(t, "RCVD", poStatus(t, pool, poUUID))
	onHand, _, _ = slabTotals(t, pool, itemUUID)
	assert.InDelta(t, 3*tenByFiveFeetSqft, onHand, 1e-9)
}

func TestOnOrderReceipt_RefusesATickThatIsNotThisLinesOnOrderSlab(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _, _ := slabPO(t, pool, 2*tenByFiveFeetSqft)
	_, otherLine, _, _ := slabPO(t, pool, tenByFiveFeetSqft)
	wh := defaultWarehouse(t, pool)
	slabs := putOnOrder(t, pool, poLineUUID, 2, slabThicknessMM)
	foreign := putOnOrder(t, pool, otherLine, 1, slabThicknessMM)

	_, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked("00000000-0000-0000-0000-000000000000")), false, 1)
	requireClientError(t, err, "not found")

	_, err = CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked("not-a-uuid")), false, 1)
	requireClientError(t, err, "not found")

	_, err = CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[0]), ticked(slabs[0])), false, 1)
	requireClientError(t, err, "ticked twice")

	_, err = CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(foreign[0])), false, 1)
	requireClientError(t, err, "not ordered on this purchase order line")

	// Once a slab has arrived it cannot arrive again.
	_, err = CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[0])), false, 1)
	require.NoError(t, err)
	_, err = CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[0])), false, 1)
	requireClientError(t, err, "no longer on order")
}

func TestOnOrderReceipt_ThicknessIsRequiredOnlyWhenTheOrderLeftItOut(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID, poNumber := slabPO(t, pool, 2*tenByFiveFeetSqft)
	wh := defaultWarehouse(t, pool)
	slabs := putOnOrder(t, pool, poLineUUID, 2, 0) // thickness unknown while on order

	_, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[0])), false, 1)
	requireClientError(t, err, "thickness")
	assert.Equal(t, map[string]string{poNumber + "-001": "on_order", poNumber + "-002": "on_order"},
		slabStatuses(t, pool, itemUUID), "a refused receipt leaves everything on order")

	withThickness := ticked(slabs[0])
	withThickness.ThicknessMM = 25
	_, err = CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, withThickness), false, 1)
	require.NoError(t, err)
	unit, err := inventory.GetUnit(ctx, pool, slabs[0])
	require.NoError(t, err)
	assert.InDelta(t, 25, unit.ThicknessMM, 1e-9)

	// A thickness the order DID state is never overridden by the receiver.
	poUUID2, poLineUUID2, _, _ := slabPO(t, pool, tenByFiveFeetSqft)
	stated := putOnOrder(t, pool, poLineUUID2, 1, slabThicknessMM)
	override := ticked(stated[0])
	override.ThicknessMM = 99
	_, err = CreateAndPost(ctx, pool, slabReceipt(poUUID2, poLineUUID2, wh, override), false, 1)
	require.NoError(t, err)
	unit, err = inventory.GetUnit(ctx, pool, stated[0])
	require.NoError(t, err)
	assert.InDelta(t, slabThicknessMM, unit.ThicknessMM, 1e-9)
}

func TestOnOrderReceipt_TickedAndTypedSlabsShareOneReceipt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID, poNumber := slabPO(t, pool, 3*tenByFiveFeetSqft)
	wh := defaultWarehouse(t, pool)
	slabs := putOnOrder(t, pool, poLineUUID, 2, slabThicknessMM)

	// One slab the order listed, and one the vendor sent on top of it.
	ir, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[0]), fullSlab()), false, 1)
	require.NoError(t, err)
	require.Len(t, ir.Items[0].Slabs, 2)
	assert.InDelta(t, 2*tenByFiveFeetSqft, ir.Items[0].QtyReceived, 1e-9)

	assert.Equal(t, poNumber+"-001", ir.Items[0].Slabs[0].Serial)
	assert.Equal(t, poNumber+"-003", ir.Items[0].Slabs[1].Serial, "the typed slab continues after the highest serial, on-order ones included")
	assert.Nil(t, ir.Items[0].Slabs[1].OnOrderSlabID)
	assert.Equal(t, map[string]string{
		poNumber + "-001": "available",
		poNumber + "-002": "on_order",
		poNumber + "-003": "available",
	}, slabStatuses(t, pool, itemUUID))
}

func TestOnOrderReceipt_PendingReceiptHoldsTheTickUntilItIsPosted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID, poNumber := slabPO(t, pool, 2*tenByFiveFeetSqft)
	wh := defaultWarehouse(t, pool)
	slabs := putOnOrder(t, pool, poLineUUID, 2, slabThicknessMM)

	ir, err := Create(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[1])), 1)
	require.NoError(t, err)
	assert.Equal(t, "PEND", ir.StatusCode)
	got := ir.Items[0].Slabs[0]
	assert.Equal(t, poNumber+"-002", got.Serial, "a ticked slab shows its serial before posting")
	require.NotNil(t, got.OnOrderSlabID)
	assert.Equal(t, slabs[1], *got.OnOrderSlabID)
	assert.Nil(t, got.UnitID, "not yet received")
	assert.Equal(t, map[string]string{poNumber + "-001": "on_order", poNumber + "-002": "on_order"},
		slabStatuses(t, pool, itemUUID), "saving without posting changes nothing in Inventory")

	// Editing the pending receipt can change which slab is ticked.
	edit := slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[0]))
	ir, err = Update(ctx, pool, ir.ID, UpdateItemReceiptInput{itemReceiptFields: edit.itemReceiptFields}, 1)
	require.NoError(t, err)
	require.Equal(t, slabs[0], *ir.Items[0].Slabs[0].OnOrderSlabID)

	ir, err = Post(ctx, pool, ir.ID, PostInput{}, false, 1)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{poNumber + "-001": "available", poNumber + "-002": "on_order"},
		slabStatuses(t, pool, itemUUID))
	assert.Equal(t, "available", ir.Items[0].Slabs[0].UnitStatus)
}

func TestOnOrderReceipt_VoidScrapsWhatArrived(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, itemUUID, poNumber := slabPO(t, pool, 3*tenByFiveFeetSqft)
	wh := defaultWarehouse(t, pool)
	slabs := putOnOrder(t, pool, poLineUUID, 3, slabThicknessMM)

	ir, err := CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[0]), ticked(slabs[1])), false, 1)
	require.NoError(t, err)

	_, err = Void(ctx, pool, ir.ID, VoidInput{VoidReason: "wrong shipment"}, 1)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		poNumber + "-001": "scrapped",
		poNumber + "-002": "scrapped",
		poNumber + "-003": "on_order",
	}, slabStatuses(t, pool, itemUUID))
	onHand, slabLedger, _ := slabTotals(t, pool, itemUUID)
	assert.Zero(t, onHand)
	assert.Zero(t, slabLedger, "received then scrapped nets to nothing")
}

func TestOnOrderSlabs_ListsWhatIsStillToArrive(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, poLineUUID, _, poNumber := slabPO(t, pool, 3*tenByFiveFeetSqft)
	wh := defaultWarehouse(t, pool)
	slabs := putOnOrder(t, pool, poLineUUID, 3, slabThicknessMM)

	list, err := OnOrderSlabs(ctx, pool, poUUID)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, poNumber+"-001", list[0].Serial)
	assert.Equal(t, slabs[0], list[0].ID)
	assert.Equal(t, poLineUUID, list[0].PurchaseOrderItemID)
	assert.InDelta(t, slabLengthMM, list[0].LengthMM, 1e-9)
	assert.InDelta(t, slabWidthMM, list[0].WidthMM, 1e-9)
	assert.InDelta(t, tenByFiveFeetSqft, list[0].Area, 1e-9)

	_, err = CreateAndPost(ctx, pool, slabReceipt(poUUID, poLineUUID, wh, ticked(slabs[0])), false, 1)
	require.NoError(t, err)
	list, err = OnOrderSlabs(ctx, pool, poUUID)
	require.NoError(t, err)
	assert.Len(t, list, 2, "an arrived slab drops off the list")

	_, err = OnOrderSlabs(ctx, pool, "00000000-0000-0000-0000-000000000000")
	requireClientError(t, err, "Unknown purchase order")
}
