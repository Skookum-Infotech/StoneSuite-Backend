//go:build dbtest

package fabrication

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/inventory"
)

func assertCuttingPreflight(t *testing.T, pool *pgxpool.Pool, jobUUID, revision, slab string) {
	t.Helper()
	ctx := context.Background()
	var warehouse string
	var jobID int
	require.NoError(t, pool.QueryRow(ctx, `SELECT company_location_uuid FROM company_location WHERE company_location_id=1`).Scan(&warehouse))
	require.NoError(t, pool.QueryRow(ctx, `SELECT fabrication_job_id FROM fabrication_job WHERE fabrication_job_uuid=$1`, jobUUID).Scan(&jobID))
	bin, err := inventory.CreateBin(ctx, pool, inventory.BinInput{WarehouseUUID: warehouse, Code: testActionUUID()[:20], Type: "floor", IsActive: true, IsWIP: true}, 1)
	require.NoError(t, err)
	for _, scenario := range []string{"ready", "empty", "duplicate", "wrong bin", "uninspected", "released", "stale order", "unapproved", "storage bin", "inactive bin", "invalid layout", "other job"} {
		t.Run("cutting preflight/"+scenario, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { require.NoError(t, tx.Rollback(ctx)) }()
			_, err = tx.Exec(ctx, `UPDATE inventory_slab SET inventory_bin_id=(SELECT inventory_bin_id FROM inventory_bin WHERE inventory_bin_uuid=$1) WHERE inventory_slab_uuid=$2`, bin.ID, slab)
			require.NoError(t, err)
			ids := []string{slab}
			targetJob := jobID
			switch scenario {
			case "empty":
				ids = nil
			case "duplicate":
				ids = append(ids, slab)
			case "wrong bin":
				_, err = tx.Exec(ctx, `UPDATE inventory_slab SET inventory_bin_id=NULL WHERE inventory_slab_uuid=$1`, slab)
			case "uninspected":
				_, err = tx.Exec(ctx, `UPDATE inventory_slab SET inspection_status='pending' WHERE inventory_slab_uuid=$1`, slab)
			case "released":
				_, err = tx.Exec(ctx, `UPDATE fabrication_job_slab SET allocation_status='released' WHERE fabrication_job_id=$1`, jobID)
			case "stale order":
				_, err = tx.Exec(ctx, `UPDATE sales_order SET sales_order_record_version=sales_order_record_version+1 WHERE sales_order_id=(SELECT sales_order_id FROM fabrication_job WHERE fabrication_job_id=$1)`, jobID)
			case "unapproved":
				_, err = tx.Exec(ctx, `UPDATE fabrication_template_revision SET state='rejected' WHERE template_uuid=$1`, revision)
			case "storage bin":
				_, err = tx.Exec(ctx, `UPDATE inventory_bin SET bin_is_wip=false WHERE inventory_bin_uuid=$1`, bin.ID)
			case "inactive bin":
				_, err = tx.Exec(ctx, `UPDATE inventory_bin SET bin_is_active=false WHERE inventory_bin_uuid=$1`, bin.ID)
			case "invalid layout":
				_, err = tx.Exec(ctx, `UPDATE fabrication_material_layout SET layout='{}' WHERE template_id=(SELECT template_id FROM fabrication_template_revision WHERE template_uuid=$1)`, revision)
			case "other job":
				targetJob = -1
			}
			require.NoError(t, err)
			got, err := lockCuttingMaterials(ctx, tx, targetJob, revision, bin.ID, ids)
			if scenario != "ready" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, slab, got[0].SlabUUID)
			require.Equal(t, "Island", got[0].Line.Pieces[0].Name)
			require.Len(t, got[0].Layout.Placements, 1)
		})
	}
}
