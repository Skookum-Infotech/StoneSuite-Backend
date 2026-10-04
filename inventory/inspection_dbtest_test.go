//go:build dbtest

package inventory

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRejectedInspectionRemainsUnavailable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("INSPECT"))
	unit, err := CreateUnit(ctx, pool, CreateUnitInput{Serial: uniq("INSPECT"), InventoryItemUUID: item, WarehouseID: 1, LengthMM: 2000, WidthMM: 1000, ThicknessMM: 30}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE inventory_slab SET inspection_status='pending' WHERE inventory_slab_uuid=$1`, unit.ID)
	require.NoError(t, err)
	var itemID int
	require.NoError(t, pool.QueryRow(ctx, `SELECT inventory_item_id FROM inventory_item WHERE inventory_item_uuid=$1`, item).Scan(&itemID))
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	stock, _, err := stockPosition(ctx, tx, []int{itemID})
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	require.Zero(t, stock[itemID], "pending material must not be promised to an order")
	err = InspectUnit(ctx, pool, unit.ID, InspectionInput{Decision: "rejected", Reason: "Cracked on arrival"}, 1)
	require.NoError(t, err)
	_, err = CutUnit(ctx, pool, unit.ID, CutInput{}, 1)
	require.Error(t, err)
	err = InspectUnit(ctx, pool, unit.ID, InspectionInput{Decision: "accepted"}, 1)
	require.Error(t, err, "rejected slabs must be replaced, not silently accepted")
	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT inspection_status FROM inventory_slab WHERE inventory_slab_uuid=$1`, unit.ID).Scan(&status))
	require.Equal(t, "rejected", status)
}
