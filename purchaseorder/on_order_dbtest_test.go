// purchaseorder/on_order_dbtest_test.go — sending an order puts its slabs on
// order; ending it before they arrive takes them back off.
//go:build dbtest

package purchaseorder

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/dbtestutil"
)

type onOrderSlab struct {
	serial, status                 string
	lengthMM, widthMM, thicknessMM float64
	area                           float64
	hasLine                        bool
}

// slabsFor reads an item's slabs in serial order.
func slabsFor(t *testing.T, pool *pgxpool.Pool, itemUUID string) []onOrderSlab {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT s.slab_serial, s.slab_status, s.slab_length_mm, s.slab_width_mm, s.slab_thickness_mm, s.slab_area,
		       s.purchase_order_item_id IS NOT NULL
		FROM inventory_slab s JOIN inventory_item i ON i.inventory_item_id = s.inventory_item_id
		WHERE i.inventory_item_uuid = $1 ORDER BY s.slab_serial`, itemUUID)
	require.NoError(t, err)
	defer rows.Close()
	var out []onOrderSlab
	for rows.Next() {
		var s onOrderSlab
		require.NoError(t, rows.Scan(&s.serial, &s.status, &s.lengthMM, &s.widthMM, &s.thicknessMM, &s.area, &s.hasLine))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// ledgerRows counts every slab-ledger row an item has: an On Order slab is not
// stock, so a sent order must leave this at zero.
func ledgerRows(t *testing.T, pool *pgxpool.Pool, itemUUID string) (rows int, onHand float64) {
	t.Helper()
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT (SELECT COUNT(*) FROM inventory_slab_ledger l
		        JOIN inventory_item i ON i.inventory_item_id = l.inventory_item_id
		        WHERE i.inventory_item_uuid = $1),
		       COALESCE((SELECT SUM(s.quantity_on_hand) FROM inventory_stock s
		        JOIN inventory_item i ON i.inventory_item_id = s.inventory_item_id
		        WHERE i.inventory_item_uuid = $1), 0)`, itemUUID).Scan(&rows, &onHand))
	return rows, onHand
}

func sizedSlabLine(itemUUID string, slabs int, thickness *float64) LineInput {
	length, width := 120.0, 60.0
	return LineInput{
		LineNumber: 1, InventoryItemUUID: itemUUID,
		Length: &length, Width: &width, Thickness: thickness,
		ExpectedSlabs: intPtr(slabs), DimensionUnit: "in",
	}
}

func TestSend_PutsTheOrdersSlabsOnOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dbtestutil.EnsureLocation(t, pool)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	makeSlabItem(t, itemUUID)

	thickness := 0.75
	po, err := Create(ctx, pool, slabOrder(vendorUUID, sizedSlabLine(itemUUID, 3, &thickness)), 1)
	require.NoError(t, err)
	require.NotNil(t, po.Items[0].Thickness)
	assert.InDelta(t, 0.75, *po.Items[0].Thickness, 1e-9, "the line keeps its thickness")
	assert.InDelta(t, 150, po.Items[0].Quantity, 1e-9, "3 slabs of 120 x 60 in is 150 sq ft")
	assert.Empty(t, slabsFor(t, pool, itemUUID), "a draft order has put nothing on order")

	sent, err := Transition(ctx, pool, po.ID, "SENT", 1)
	require.NoError(t, err)
	assert.Equal(t, "SENT", sent.StatusCode)
	assert.Equal(t, 3, sent.Items[0].SlabsOnOrder)

	slabs := slabsFor(t, pool, itemUUID)
	require.Len(t, slabs, 3)
	for i, s := range slabs {
		assert.Equal(t, po.Number+"-00"+string(rune('1'+i)), s.serial)
		assert.Equal(t, "on_order", s.status)
		assert.InDelta(t, 3048, s.lengthMM, 1e-9, "120 in")
		assert.InDelta(t, 1524, s.widthMM, 1e-9, "60 in")
		assert.InDelta(t, 19.05, s.thicknessMM, 1e-9, "0.75 in")
		assert.InDelta(t, 50, s.area, 1e-9)
		assert.True(t, s.hasLine, "each slab knows which order line it was minted for")
	}

	rows, onHand := ledgerRows(t, pool, itemUUID)
	assert.Zero(t, rows, "an On Order slab is not stock: no ledger row")
	assert.Zero(t, onHand, "and nothing is on hand")
}

func TestSend_ThicknessFallsBackToTheItem(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dbtestutil.EnsureLocation(t, pool)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	makeSlabItem(t, itemUUID)
	_, err := pool.Exec(ctx, `UPDATE inventory_item SET inventory_item_thickness_mm = 30 WHERE inventory_item_uuid = $1`, itemUUID)
	require.NoError(t, err)

	po, err := Create(ctx, pool, slabOrder(vendorUUID, sizedSlabLine(itemUUID, 2, nil)), 1)
	require.NoError(t, err)
	_, err = Transition(ctx, pool, po.ID, "SENT", 1)
	require.NoError(t, err)

	slabs := slabsFor(t, pool, itemUUID)
	require.Len(t, slabs, 2)
	for _, s := range slabs {
		assert.InDelta(t, 30, s.thicknessMM, 1e-9, "the item's own thickness")
	}
}

func TestSend_ThicknessMayBeUnknownWhileOnOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dbtestutil.EnsureLocation(t, pool)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	makeSlabItem(t, itemUUID)

	po, err := Create(ctx, pool, slabOrder(vendorUUID, sizedSlabLine(itemUUID, 1, nil)), 1)
	require.NoError(t, err)
	_, err = Transition(ctx, pool, po.ID, "SENT", 1)
	require.NoError(t, err, "neither the line nor the item states a thickness; the slab is still minted")

	slabs := slabsFor(t, pool, itemUUID)
	require.Len(t, slabs, 1)
	assert.Zero(t, slabs[0].thicknessMM, "the receipt that brings it in supplies the thickness")
}

func TestSend_OnlySizedSlabLinesPutAnythingOnOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dbtestutil.EnsureLocation(t, pool)
	vendorUUID, slabItem := seedVendorAndItem(t, pool)
	makeSlabItem(t, slabItem)
	_, plainItem := seedVendorAndItem(t, pool) // a plain quantity item
	_, unsizedSlabItem := seedVendorAndItem(t, pool)
	makeSlabItem(t, unsizedSlabItem)

	sized := sizedSlabLine(slabItem, 2, nil)
	plain := LineInput{LineNumber: 2, InventoryItemUUID: plainItem, Quantity: 5}
	unsized := LineInput{LineNumber: 3, InventoryItemUUID: unsizedSlabItem, Quantity: 40}
	po, err := Create(ctx, pool, slabOrder(vendorUUID, sized, plain, unsized), 1)
	require.NoError(t, err)
	_, err = Transition(ctx, pool, po.ID, "SENT", 1)
	require.NoError(t, err)

	assert.Len(t, slabsFor(t, pool, slabItem), 2)
	assert.Empty(t, slabsFor(t, pool, plainItem), "a quantity item has no slabs to put on order")
	assert.Empty(t, slabsFor(t, pool, unsizedSlabItem), "a slab line that states no size keeps manual slab entry on the receipt")
}

func TestAllocateOnOrderSlabs_RunTwiceMintsOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dbtestutil.EnsureLocation(t, pool)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	makeSlabItem(t, itemUUID)

	po, err := Create(ctx, pool, slabOrder(vendorUUID, sizedSlabLine(itemUUID, 3, nil)), 1)
	require.NoError(t, err)
	_, err = Transition(ctx, pool, po.ID, "SENT", 1)
	require.NoError(t, err)

	var internalID int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT purchase_order_id FROM purchase_order WHERE purchase_order_uuid = $1`, po.ID).Scan(&internalID))
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, allocateOnOrderSlabs(ctx, tx, internalID, 1))
	require.NoError(t, tx.Commit(ctx))

	assert.Len(t, slabsFor(t, pool, itemUUID), 3, "a line that already has slabs is left alone")
}

func TestEndingAnOrder_ScrapsTheSlabsThatNeverArrived(t *testing.T) {
	for _, toStatus := range []string{"CANC", "CLSD"} {
		t.Run(toStatus, func(t *testing.T) {
			pool := testPool(t)
			ctx := context.Background()
			dbtestutil.EnsureLocation(t, pool)
			vendorUUID, itemUUID := seedVendorAndItem(t, pool)
			makeSlabItem(t, itemUUID)

			po, err := Create(ctx, pool, slabOrder(vendorUUID, sizedSlabLine(itemUUID, 3, nil)), 1)
			require.NoError(t, err)
			_, err = Transition(ctx, pool, po.ID, "SENT", 1)
			require.NoError(t, err)

			ended, err := Transition(ctx, pool, po.ID, toStatus, 1)
			require.NoError(t, err)
			assert.Equal(t, toStatus, ended.StatusCode)
			assert.Zero(t, ended.Items[0].SlabsOnOrder)

			slabs := slabsFor(t, pool, itemUUID)
			require.Len(t, slabs, 3)
			for _, s := range slabs {
				assert.Equal(t, "scrapped", s.status)
			}
			rows, onHand := ledgerRows(t, pool, itemUUID)
			assert.Zero(t, rows, "they never entered stock, so writing them off must not touch the ledger")
			assert.Zero(t, onHand)
		})
	}
}

func TestCreate_ThicknessIsValidated(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID, slabItem := seedVendorAndItem(t, pool)
	makeSlabItem(t, slabItem)
	_, plainItem := seedVendorAndItem(t, pool)

	zero, tooThick, ok := 0.0, 500.0, 0.75
	tests := []struct {
		name    string
		line    LineInput
		wantErr string
	}{
		{name: "zero", line: sizedSlabLine(slabItem, 1, &zero), wantErr: "greater than zero"},
		{name: "past a metre", line: sizedSlabLine(slabItem, 1, &tooThick), wantErr: "cannot exceed 1 m"},
		{name: "on a line that is not measured by area",
			line: LineInput{LineNumber: 1, InventoryItemUUID: plainItem, Quantity: 5, Thickness: &ok}, wantErr: "SQFT or SQM"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Create(ctx, pool, slabOrder(vendorUUID, tc.line), 1)
			require.Error(t, err)
			assert.True(t, IsClientError(err))
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
