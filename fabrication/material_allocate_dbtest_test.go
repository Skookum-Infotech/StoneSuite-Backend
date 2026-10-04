//go:build dbtest

package fabrication

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/inventory"
	"testing"
)

func TestMaterialAllocationPersistsLayoutAndReplays(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET workflow_version=2,delivery_mode='supply_only' WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	var item string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO inventory_item(inventory_item_sku,inventory_item_name,inventory_item_unit_id,inventory_item_tracking) SELECT $1,'Stone',unit_id,'serialized' FROM lkp_unit WHERE unit_code='SQFT' RETURNING inventory_item_uuid`, testActionUUID()).Scan(&item))
	makeUnit := func() string {
		unit, err := inventory.CreateUnit(ctx, pool, inventory.CreateUnitInput{Serial: testActionUUID(), InventoryItemUUID: item, WarehouseID: 1, LengthMM: 2000, WidthMM: 1000, ThicknessMM: 30}, 1)
		require.NoError(t, err)
		require.NoError(t, inventory.InspectUnit(ctx, pool, unit.ID, inventory.InspectionInput{Decision: "accepted"}, 1))
		return unit.ID
	}
	firstSlab, secondSlab := makeUnit(), makeUnit()
	line := TemplateLine{SourceLineID: testActionUUID(), MaterialID: item, Pieces: []MeasuredPiece{{Name: "Island", LengthMM: 1500, WidthMM: 600, ThicknessMM: 30}}}
	raw, err := json.Marshal([]TemplateLine{line})
	require.NoError(t, err)
	var revision string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO fabrication_template_revision(fabrication_job_id,revision,sales_order_version,state,baseline,measured_lines,change_summary) SELECT fabrication_job_id,1,1,'approved','[]',$2,'{}' FROM fabrication_job WHERE fabrication_job_uuid=$1 RETURNING template_uuid`, job.ID, raw).Scan(&revision))
	actor := ActionActor{IdentityID: testActionUUID(), EmployeeID: 1, AllScope: true}
	in := AllocateMaterialInput{CommandMeta: CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()}, SlabID: firstSlab, TemplateID: revision, SourceLineID: line.SourceLineID, Layout: MaterialLayout{Placements: []PiecePlacement{{PieceIndex: 0}}, KerfMM: 3, SuitabilityConfirmed: true, ReviewNote: "Grain and defects reviewed"}}
	_, err = AllocateMaterial(ctx, pool, job.ID, actor, in)
	require.ErrorIs(t, err, ErrActionPermission)
	denied := actor
	denied.AllScope = false
	_, err = allocateMaterial(ctx, pool, job.ID, denied, in)
	require.ErrorIs(t, err, ErrNotFound)
	invalid := in
	invalid.Layout.Placements = []PiecePlacement{{PieceIndex: 0, XMM: 1000}}
	_, err = allocateMaterial(ctx, pool, job.ID, actor, invalid)
	require.Error(t, err)
	unit, err := inventory.GetUnit(ctx, pool, firstSlab)
	require.NoError(t, err)
	require.Equal(t, "available", unit.Status)
	result, err := allocateMaterial(ctx, pool, job.ID, actor, in)
	require.NoError(t, err)
	replay, err := allocateMaterial(ctx, pool, job.ID, actor, in)
	require.NoError(t, err)
	require.Equal(t, result, replay)
	changed := in
	changed.Layout.ReviewNote = "changed"
	_, err = allocateMaterial(ctx, pool, job.ID, actor, changed)
	require.ErrorIs(t, err, ErrActionConflict)
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM fabrication_material_layout l JOIN fabrication_job_slab a USING(fabrication_job_slab_id) JOIN fabrication_job j USING(fabrication_job_id) WHERE j.fabrication_job_uuid=$1`, job.ID).Scan(&count))
	require.Equal(t, 1, count)
	assertCuttingPreflight(t, pool, job.ID, revision, firstSlab)
	next := in
	next.RequestID = testActionUUID()
	next.ExpectedVersion = result.Version
	next.SlabID = secondSlab
	_, err = allocateMaterial(ctx, pool, job.ID, actor, next)
	require.ErrorContains(t, err, "already has")
	unit, err = inventory.GetUnit(ctx, pool, secondSlab)
	require.NoError(t, err)
	require.Equal(t, "available", unit.Status)
	_, err = pool.Exec(ctx, `UPDATE sales_order SET sales_order_record_version=sales_order_record_version+1 WHERE sales_order_uuid=$1`, job.SalesOrderID)
	require.NoError(t, err)
	_, err = allocateMaterial(ctx, pool, job.ID, actor, next)
	require.ErrorIs(t, err, ErrActionConflict)
	require.ErrorIs(t, AllocateSlab(ctx, pool, job.ID, secondSlab, "", 1), ErrActionConflict)
	require.ErrorIs(t, DeallocateSlab(ctx, pool, job.ID, firstSlab, 1), ErrActionConflict)
	release := ReleaseMaterialInput{CommandMeta: CommandMeta{ExpectedVersion: result.Version, RequestID: testActionUUID()}, SlabID: firstSlab}
	_, err = ReleaseMaterial(ctx, pool, job.ID, actor, release)
	require.ErrorIs(t, err, ErrActionPermission)
	freed, err := releaseMaterial(ctx, pool, job.ID, actor, release)
	require.NoError(t, err)
	again, err := releaseMaterial(ctx, pool, job.ID, actor, release)
	require.NoError(t, err)
	require.Equal(t, freed, again)
	unit, err = inventory.GetUnit(ctx, pool, firstSlab)
	require.NoError(t, err)
	require.Equal(t, "available", unit.Status)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM fabrication_material_layout l JOIN fabrication_job_slab a USING(fabrication_job_slab_id) JOIN fabrication_job j USING(fabrication_job_id) WHERE j.fabrication_job_uuid=$1`, job.ID).Scan(&count))
	require.Equal(t, 1, count, "released layouts remain evidence")

}
