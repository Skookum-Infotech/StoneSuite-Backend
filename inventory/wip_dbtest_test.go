//go:build dbtest

package inventory

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWIPMoveRequiresAcceptedMaterialAndWIPDestination(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var warehouse string
	require.NoError(t, pool.QueryRow(ctx, `SELECT company_location_uuid FROM company_location WHERE company_location_id=1`).Scan(&warehouse))
	for _, tc := range []struct {
		name, inspection, machine string
		wip, valid                bool
	}{
		{"shared", "accepted", "", true, true}, {"saw", "accepted", "Saw 1", true, true},
		{"pending", "pending", "", true, false}, {"rejected", "rejected", "", true, false},
		{"legacy needs inspection", "legacy", "", true, false}, {"storage", "accepted", "", false, false},
		{"inactive WIP", "accepted", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := seedAreaItem(t, pool, uniq("WIP"))
			unit, err := CreateUnit(ctx, pool, CreateUnitInput{Serial: uniq("WIP"), InventoryItemUUID: item, WarehouseID: 1, LengthMM: 2000, WidthMM: 1000, ThicknessMM: 30}, 1)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `UPDATE inventory_slab SET inspection_status=$2 WHERE inventory_slab_uuid=$1`, unit.ID, tc.inspection)
			require.NoError(t, err)
			bin, err := CreateBin(ctx, pool, BinInput{WarehouseUUID: warehouse, Code: uniq("WIP"), Type: "floor", IsActive: true, IsWIP: tc.wip, MachineLabel: tc.machine}, 1)
			require.NoError(t, err)
			if tc.name == "inactive WIP" {
				_, err = pool.Exec(ctx, `UPDATE inventory_bin SET bin_is_active=false WHERE inventory_bin_uuid=$1`, bin.ID)
				require.NoError(t, err)
			}
			tx, err := pool.Begin(ctx)
			require.NoError(t, err)
			err = MoveInspectedUnitToWIPTx(ctx, tx, unit.ID, bin.ID, "Ready for cutting", 1)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NoError(t, tx.Rollback(ctx))
			got, err := GetUnit(ctx, pool, unit.ID)
			require.NoError(t, err)
			require.NotEqual(t, &bin.ID, got.BinID, "caller rollback must undo the physical movement")
		})
	}
}
