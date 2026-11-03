// inventory/on_order_dbtest_test.go — a slab that is still on order is not stone
// anyone can handle.
//go:build dbtest

package inventory

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// onOrderUnit makes a unit and flips it to 'on_order'. The flip is done in SQL
// so the guards are tested on their own, without a purchase order behind the
// slab; the stock effect of the unit's own receipt is irrelevant to them.
func onOrderUnit(t *testing.T) (uuid string, id int) {
	t.Helper()
	pool := testPool(t)
	item := seedAreaItem(t, pool, uniq("DBTEST-ONORDER"))
	u, err := CreateUnit(context.Background(), pool, CreateUnitInput{
		Serial: uniq("DBTEST-OO"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30,
	}, 1)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(context.Background(), `
		UPDATE inventory_slab SET slab_status = 'on_order' WHERE inventory_slab_uuid = $1
		RETURNING inventory_slab_id`, u.ID).Scan(&id))
	return u.ID, id
}

func inTx(t *testing.T, fn func(tx pgx.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := testPool(t).Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	fn(tx)
}

func requireStillOnOrder(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.True(t, IsClientError(err), "must be a 400, not a 500: %v", err)
	assert.Contains(t, err.Error(), "on order")
}

func TestOnOrderUnit_CannotBeMovedCutBundledOrTransferred(t *testing.T) {
	ctx := context.Background()
	uuid, _ := onOrderUnit(t)

	inTx(t, func(tx pgx.Tx) {
		requireStillOnOrder(t, MoveUnitToBinTx(ctx, tx, uuid, MoveUnitInput{}, 1))
	})

	inTx(t, func(tx pgx.Tx) {
		_, err := CutUnitTx(ctx, tx, uuid, CutInput{}, 1)
		requireStillOnOrder(t, err)
	})

	inTx(t, func(tx pgx.Tx) {
		ref, err := ResolveUnitForDocument(ctx, tx, uuid, true)
		require.NoError(t, err)
		assert.Equal(t, StatusOnOrder, ref.Status)

		requireStillOnOrder(t, ShipSlabForTransfer(ctx, tx, ref, ref.WarehouseID, DocSource{}, 1))

		_, err = AdjustSlabDown(ctx, tx, ref, nil, "", DocSource{}, 1)
		requireStillOnOrder(t, err)

		_, err = AdjustSlabUp(ctx, tx, ref, nil, "", DocSource{}, 1)
		requireStillOnOrder(t, err)
	})
}

func TestAdjustSlabUp_RefusesASlabThatNeverArrived(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uuid, id := onOrderUnit(t)

	// A cancelled order's slab: scrapped, and it never had a 'received' ledger row.
	_, err := pool.Exec(ctx, `DELETE FROM inventory_slab_ledger WHERE inventory_slab_id = $1`, id)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE inventory_slab SET slab_status = 'scrapped' WHERE inventory_slab_id = $1`, id)
	require.NoError(t, err)

	inTx(t, func(tx pgx.Tx) {
		ref, err := ResolveUnitForDocument(ctx, tx, uuid, true)
		require.NoError(t, err)
		_, err = AdjustSlabUp(ctx, tx, ref, nil, "", DocSource{}, 1)
		require.Error(t, err)
		assert.True(t, IsClientError(err))
		assert.Contains(t, err.Error(), "never arrived")
	})
}
