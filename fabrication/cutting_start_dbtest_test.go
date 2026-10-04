//go:build dbtest

package fabrication

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/inventory"
	"testing"
)

func TestCuttingStartReplayAndGuards(t *testing.T) {
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
	firstSlab := makeUnit()
	line := TemplateLine{SourceLineID: testActionUUID(), MaterialID: item, Pieces: []MeasuredPiece{{Name: "Island", LengthMM: 1500, WidthMM: 600, ThicknessMM: 30}}}
	raw, err := json.Marshal([]TemplateLine{line})
	require.NoError(t, err)
	var revision string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO fabrication_template_revision(fabrication_job_id,revision,sales_order_version,state,baseline,measured_lines,change_summary) SELECT fabrication_job_id,1,1,'approved','[]',$2,'{}' FROM fabrication_job WHERE fabrication_job_uuid=$1 RETURNING template_uuid`, job.ID, raw).Scan(&revision))
	actor := ActionActor{IdentityID: testActionUUID(), EmployeeID: 1, AllScope: true}
	in := AllocateMaterialInput{CommandMeta: CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()}, SlabID: firstSlab, TemplateID: revision, SourceLineID: line.SourceLineID, Layout: MaterialLayout{Placements: []PiecePlacement{{PieceIndex: 0}}, KerfMM: 3, SuitabilityConfirmed: true, ReviewNote: "Grain and defects reviewed"}}

	allocated, err := allocateMaterial(ctx, pool, job.ID, actor, in)
	require.NoError(t, err)
	var warehouse string
	require.NoError(t, pool.QueryRow(ctx, `SELECT company_location_uuid FROM company_location WHERE company_location_id=1`).Scan(&warehouse))
	bin, err := inventory.CreateBin(ctx, pool, inventory.BinInput{WarehouseUUID: warehouse, Code: testActionUUID()[:20], Type: "floor", IsActive: true, IsWIP: true}, 1)
	require.NoError(t, err)
	moved, err := transferToWIP(ctx, pool, job.ID, actor, WIPTransferInput{CommandMeta: CommandMeta{ExpectedVersion: allocated.Version, RequestID: testActionUUID()}, SlabID: firstSlab, BinID: bin.ID})
	require.NoError(t, err)
	bundle, err := inventory.CreateBundle(ctx, pool, inventory.BundleInput{Code: testActionUUID()[:20], WarehouseID: 1, BinUUID: &bin.ID, MemberIDs: []string{firstSlab}}, 1)
	require.NoError(t, err)
	start := StartCuttingInput{CommandMeta: CommandMeta{ExpectedVersion: moved.Version, RequestID: testActionUUID()}, TemplateID: revision, BinID: bin.ID, SlabIDs: []string{firstSlab}}
	_, err = StartCuttingRun(ctx, pool, job.ID, actor, start)
	require.ErrorIs(t, err, ErrActionPermission)
	denied := actor
	denied.AllScope = false
	_, err = startCuttingRun(ctx, pool, job.ID, denied, start)
	require.ErrorIs(t, err, ErrNotFound)
	result, err := startCuttingRun(ctx, pool, job.ID, actor, start)
	require.NoError(t, err)
	require.NotEmpty(t, result.RelatedID)
	replay, err := startCuttingRun(ctx, pool, job.ID, actor, start)
	require.NoError(t, err)
	require.Equal(t, result, replay)
	again := start
	again.RequestID = testActionUUID()
	again.ExpectedVersion = result.Version
	_, err = startCuttingRun(ctx, pool, job.ID, actor, again)
	require.ErrorIs(t, err, ErrActionConflict)
	var runs, pieces, consumption int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM fabrication_cutting_run r JOIN fabrication_job j USING(fabrication_job_id) WHERE j.fabrication_job_uuid=$1`, job.ID).Scan(&runs))
	require.Equal(t, 1, runs)
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM fabrication_cutting_selection s JOIN fabrication_cutting_run r USING(cutting_run_id) WHERE r.cutting_run_uuid=$1`, result.RelatedID).Scan(&pieces))
	require.Equal(t, 1, pieces)
	var pieceUUID, stage, name string
	var length, width, thickness float64
	require.NoError(t, pool.QueryRow(ctx, `SELECT p.fabrication_job_item_uuid,p.production_stage,p.piece_name,p.piece_length_mm,p.piece_width_mm,p.piece_thickness_mm FROM fabrication_cutting_selection s JOIN fabrication_cutting_run r USING(cutting_run_id) JOIN fabrication_job_item p USING(fabrication_job_item_id) WHERE r.cutting_run_uuid=$1 AND p.fabrication_job_id=r.fabrication_job_id`, result.RelatedID).Scan(&pieceUUID, &stage, &name, &length, &width, &thickness))
	require.Equal(t, string(StageCutting), stage)
	require.Equal(t, "Island", name)
	require.Equal(t, 1500.0, length)
	require.Equal(t, 600.0, width)
	require.Equal(t, 30.0, thickness)
	_, err = UpdatePiece(ctx, pool, job.ID, pieceUUID, PieceInput{PieceName: "Changed"}, 1)
	require.ErrorIs(t, err, ErrPiecesLocked)
	require.ErrorIs(t, RemovePiece(ctx, pool, job.ID, pieceUUID, 1), ErrPiecesLocked)
	var productionCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM fabrication_job_item p JOIN fabrication_job j USING(fabrication_job_id) WHERE j.fabrication_job_uuid=$1`, job.ID).Scan(&productionCount))
	require.Equal(t, 1, productionCount)
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM inventory_slab_ledger l JOIN inventory_slab s USING(inventory_slab_id) WHERE s.inventory_slab_uuid=$1 AND l.event='consumed'`, firstSlab).Scan(&consumption))
	require.Zero(t, consumption)
	_, err = releaseMaterial(ctx, pool, job.ID, actor, ReleaseMaterialInput{CommandMeta: CommandMeta{ExpectedVersion: result.Version, RequestID: testActionUUID()}, SlabID: firstSlab})
	require.ErrorContains(t, err, "Started cutting")
	_, err = transferToWIP(ctx, pool, job.ID, actor, WIPTransferInput{CommandMeta: CommandMeta{ExpectedVersion: result.Version, RequestID: testActionUUID()}, SlabID: firstSlab, BinID: bin.ID})
	require.ErrorContains(t, err, "Started cutting")
	_, err = Cancel(ctx, pool, job.ID, 1)
	require.ErrorContains(t, err, "Started cutting")
	t.Run("inventory mutations preserve active inputs", func(t *testing.T) {
		require.ErrorContains(t, inventory.MoveUnitToBin(ctx, pool, firstSlab, inventory.MoveUnitInput{}, 1), "active cutting run")
		require.ErrorContains(t, inventory.ScrapUnit(ctx, pool, firstSlab, nil, "", 1), "active cutting run")
		require.ErrorContains(t, inventory.MoveBundle(ctx, pool, bundle.ID, inventory.MoveBundleInput{}, 1), "active cutting run")
		_, err := inventory.SealBundle(ctx, pool, bundle.ID, 1)
		require.ErrorContains(t, err, "active cutting run")
		_, err = inventory.BreakBundle(ctx, pool, bundle.ID, "", 1)
		require.ErrorContains(t, err, "active cutting run")
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { require.NoError(t, tx.Rollback(ctx)) }()
		ref, err := inventory.ResolveUnitForDocument(ctx, tx, firstSlab, true)
		require.NoError(t, err)
		_, err = inventory.AdjustSlabDown(ctx, tx, ref, nil, "", inventory.DocSource{}, 1)
		require.ErrorContains(t, err, "active cutting run")
	})
	var allocationStatus, bundleStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT a.allocation_status FROM fabrication_job_slab a JOIN inventory_slab s USING(inventory_slab_id) WHERE s.inventory_slab_uuid=$1`, firstSlab).Scan(&allocationStatus))
	require.Equal(t, "reserved", allocationStatus)
	require.NoError(t, pool.QueryRow(ctx, `SELECT bundle_status FROM inventory_bundle WHERE inventory_bundle_uuid=$1`, bundle.ID).Scan(&bundleStatus))
	require.Equal(t, inventory.BundleOpen, bundleStatus)
	var mutations int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM inventory_slab_ledger l JOIN inventory_slab s USING(inventory_slab_id) WHERE s.inventory_slab_uuid=$1 AND l.event IN ('scrapped','adjusted')`, firstSlab).Scan(&mutations))
	require.Zero(t, mutations)
	unit, err := inventory.GetUnit(ctx, pool, firstSlab)
	require.NoError(t, err)
	require.Equal(t, "reserved", unit.Status)
}
