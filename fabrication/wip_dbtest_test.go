//go:build dbtest

package fabrication

import (
	"context"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/inventory"
	"testing"
)

func TestWIPTransferReplayAndApproval(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET workflow_version=2,delivery_mode='supply_only' WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	var warehouse, item string
	require.NoError(t, pool.QueryRow(ctx, `SELECT company_location_uuid FROM company_location WHERE company_location_id=1`).Scan(&warehouse))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO inventory_item(inventory_item_sku,inventory_item_name,inventory_item_unit_id,inventory_item_tracking) SELECT $1,'Stone',unit_id,'serialized' FROM lkp_unit WHERE unit_code='SQFT' RETURNING inventory_item_uuid`, testActionUUID()).Scan(&item))
	unit, err := inventory.CreateUnit(ctx, pool, inventory.CreateUnitInput{Serial: testActionUUID(), InventoryItemUUID: item, WarehouseID: 1, LengthMM: 2000, WidthMM: 1000, ThicknessMM: 30}, 1)
	require.NoError(t, err)
	require.NoError(t, inventory.InspectUnit(ctx, pool, unit.ID, inventory.InspectionInput{Decision: "accepted"}, 1))
	bin, err := inventory.CreateBin(ctx, pool, inventory.BinInput{WarehouseUUID: warehouse, Code: testActionUUID()[:20], Type: "floor", IsActive: true, IsWIP: true}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO fabrication_job_slab(fabrication_job_id,inventory_slab_id,allocation_status) SELECT j.fabrication_job_id,s.inventory_slab_id,'reserved' FROM fabrication_job j,inventory_slab s WHERE j.fabrication_job_uuid=$1 AND s.inventory_slab_uuid=$2`, job.ID, unit.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE inventory_slab SET slab_status='reserved' WHERE inventory_slab_uuid=$1`, unit.ID)
	require.NoError(t, err)
	in := WIPTransferInput{CommandMeta: CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()}, SlabID: unit.ID, BinID: bin.ID}
	actor := ActionActor{IdentityID: testActionUUID(), EmployeeID: 1, AllScope: true}
	_, err = transferToWIP(ctx, pool, job.ID, actor, in)
	require.ErrorIs(t, err, ErrApprovalRequired)
	_, err = pool.Exec(ctx, `INSERT INTO fabrication_template_revision(fabrication_job_id,revision,sales_order_version,state,baseline,measured_lines,change_summary) SELECT fabrication_job_id,1,1,'approved','[]','[]','{}' FROM fabrication_job WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	_, err = TransferToWIP(ctx, pool, job.ID, actor, in)
	require.ErrorIs(t, err, ErrActionPermission, "public action cannot trust caller-provided AllScope")
	_, err = transferToWIP(ctx, pool, job.ID, actor, in)
	require.ErrorContains(t, err, "reviewed layout")
	_, err = pool.Exec(ctx, `INSERT INTO fabrication_material_layout(fabrication_job_slab_id,template_id,source_line_uuid,layout,reviewed_by) SELECT a.fabrication_job_slab_id,t.template_id,gen_random_uuid(),'{}',1 FROM fabrication_job_slab a JOIN fabrication_template_revision t ON t.fabrication_job_id=a.fabrication_job_id JOIN fabrication_job j ON j.fabrication_job_id=a.fabrication_job_id WHERE j.fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	first, err := transferToWIP(ctx, pool, job.ID, actor, in)
	require.NoError(t, err)
	second, err := transferToWIP(ctx, pool, job.ID, actor, in)
	require.NoError(t, err)
	require.Equal(t, first, second)
	got, err := inventory.GetUnit(ctx, pool, unit.ID)
	require.NoError(t, err)
	require.Equal(t, &bin.ID, got.BinID)
	var moves int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM inventory_unit_history h JOIN inventory_slab s ON s.inventory_slab_id=h.inventory_slab_id WHERE s.inventory_slab_uuid=$1 AND history_action='bin_move'`, unit.ID).Scan(&moves))
	require.Equal(t, 1, moves)
	_, err = pool.Exec(ctx, `UPDATE sales_order SET sales_order_record_version=sales_order_record_version+1 WHERE sales_order_uuid=$1`, job.SalesOrderID)
	require.NoError(t, err)
	in.RequestID = testActionUUID()
	in.ExpectedVersion = first.Version
	_, err = transferToWIP(ctx, pool, job.ID, actor, in)
	require.ErrorIs(t, err, ErrActionConflict)
}
