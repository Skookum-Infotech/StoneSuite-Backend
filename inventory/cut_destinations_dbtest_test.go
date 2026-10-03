//go:build dbtest

package inventory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCutUnitToBinsTx(t *testing.T) {
	for _, scenario := range []string{"two bins", "zero remnants", "missing", "blank", "unknown", "inactive", "deleted", "frozen destination", "frozen zero remnants"} {
		t.Run(scenario, func(t *testing.T) {
			pool := testPool(t)
			ctx := context.Background()
			item := seedAreaItem(t, pool, uniq("DBTEST-DEST"))
			parent, err := CreateUnit(ctx, pool, CreateUnitInput{
				Serial: uniq("DBTEST-DEST-PARENT"), InventoryItemUUID: item, WarehouseID: 1,
				LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
			}, 1)
			require.NoError(t, err)
			stock := onHand(t, pool, item)
			var warehouse string
			require.NoError(t, pool.QueryRow(ctx, `SELECT company_location_uuid FROM company_location WHERE company_location_id=1`).Scan(&warehouse))
			bins := make([]string, 2)
			for i := range bins {
				bin, err := CreateBin(ctx, pool, BinInput{WarehouseUUID: warehouse, Code: uniq("REM"), Name: "Remnant storage", Type: "floor", IsActive: true}, 1)
				require.NoError(t, err)
				bins[i] = bin.ID
			}
			in := CutInput{Remnants: []CutPiece{
				{Serial: uniq("DBTEST-DEST-R1"), LengthMM: 500, WidthMM: 500},
				{Serial: uniq("DBTEST-DEST-R2"), LengthMM: 600, WidthMM: 500},
			}}
			tx, err := pool.Begin(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback(ctx) })
			switch scenario {
			case "zero remnants":
				in.Remnants = nil
				bins = nil
			case "missing":
				bins = bins[:1]
			case "blank":
				bins[1] = " "
			case "unknown":
				bins[1] = "00000000-0000-0000-0000-000000000000"
			case "inactive":
				_, err = tx.Exec(ctx, `UPDATE inventory_bin SET bin_is_active=false WHERE inventory_bin_uuid=$1`, bins[1])
				require.NoError(t, err)
			case "frozen destination", "frozen zero remnants":
				var binID *int
				if scenario == "frozen destination" {
					var id int
					require.NoError(t, tx.QueryRow(ctx, `SELECT inventory_bin_id FROM inventory_bin WHERE inventory_bin_uuid=$1`, bins[1]).Scan(&id))
					binID = &id
				} else {
					in.Remnants = nil
					bins = nil
				}
				_, err = tx.Exec(ctx, `INSERT INTO inventory_count(record_type, count_status, warehouse_id, inventory_bin_id, count_frozen_at, count_frozen_by)
                    VALUES ((SELECT MIN(record_type_id) FROM lkp_record_type), (SELECT MIN(record_status_id) FROM lkp_record_status), 1, $1, NOW(), 1)`, binID)
				require.NoError(t, err)
			case "deleted":
				_, err = tx.Exec(ctx, `UPDATE inventory_bin SET bin_deleted_at=NOW(), bin_deleted_by=1 WHERE inventory_bin_uuid=$1`, bins[1])
				require.NoError(t, err)
			}
			result, err := CutUnitToBinsTx(ctx, tx, parent.ID, in, bins, 1)
			if scenario != "two bins" && scenario != "zero remnants" {
				require.Error(t, err)
				require.NoError(t, tx.Rollback(ctx))
				restored, err := GetUnit(ctx, pool, parent.ID)
				require.NoError(t, err)
				require.Equal(t, parent.Status, restored.Status)
				require.Equal(t, stock, onHand(t, pool, item))
				return
			}
			require.NoError(t, err)
			require.NoError(t, tx.Commit(ctx))
			require.Len(t, result.RemnantIDs, len(bins))
			for i, id := range result.RemnantIDs {
				remnant, err := GetUnit(ctx, pool, id)
				require.NoError(t, err)
				require.NotNil(t, remnant.BinID)
				require.Equal(t, bins[i], *remnant.BinID)
				require.Equal(t, parent.ID, *remnant.ParentUnitID)
			}
			require.Equal(t, result.RecoveredArea, onHand(t, pool, item))
			require.Equal(t, onHand(t, pool, item), ledgerSum(t, pool, item))
		})
	}
}
