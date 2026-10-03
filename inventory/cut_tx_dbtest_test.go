//go:build dbtest

package inventory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCutUnitTxCallerRollback(t *testing.T) {
	for _, failDuringRecovery := range []bool{false, true} {
		name := "caller rolls back successful cut"
		if failDuringRecovery {
			name = "duplicate serial fails after first recovery"
		}
		t.Run(name, func(t *testing.T) {
			pool := testPool(t)
			ctx := context.Background()
			item := seedAreaItem(t, pool, uniq("DBTEST-CUT-TX"))
			parent, err := CreateUnit(ctx, pool, CreateUnitInput{
				Serial: uniq("DBTEST-TX-PARENT"), InventoryItemUUID: item, WarehouseID: 1,
				LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
			}, 1)
			require.NoError(t, err)
			stockBefore := onHand(t, pool, item)
			ledgerBefore := ledgerSum(t, pool, item)
			serial := uniq("DBTEST-TX-REMNANT")
			remnants := []CutPiece{{Serial: serial, LengthMM: 500, WidthMM: 500}}
			if failDuringRecovery {
				remnants = append(remnants, remnants[0])
			}
			tx, err := pool.Begin(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback(ctx) })
			result, err := CutUnitTx(ctx, tx, parent.ID, CutInput{Remnants: remnants}, 1)
			if failDuringRecovery {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Len(t, result.RemnantIDs, 1)
				var status string
				require.NoError(t, tx.QueryRow(ctx, `SELECT slab_status FROM inventory_slab WHERE inventory_slab_uuid=$1`, parent.ID).Scan(&status))
				require.Equal(t, "consumed", status)
			}
			require.NoError(t, tx.Rollback(ctx))
			restored, err := GetUnit(ctx, pool, parent.ID)
			require.NoError(t, err)
			require.Equal(t, parent.Status, restored.Status)
			require.Equal(t, stockBefore, onHand(t, pool, item))
			require.Equal(t, ledgerBefore, ledgerSum(t, pool, item))
			var count int
			require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM inventory_slab WHERE slab_serial=$1`, serial).Scan(&count))
			require.Zero(t, count)
		})
	}
}
