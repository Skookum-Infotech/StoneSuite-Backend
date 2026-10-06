// fabrication/materials_test.go
//go:build dbtest

package fabrication

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
	"stonesuite-backend/salesorder"
)

const slabSqFt = 45.208 // a 3000 x 1400 mm slab, in square feet

func uniqStr(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }

func seedSlabItem(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var uuid string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'serialized' FROM lkp_unit u WHERE u.unit_code = 'SQFT'
		RETURNING inventory_item_uuid`, uniqStr("MAT")).Scan(&uuid); err != nil {
		t.Fatalf("seed slab item: %v", err)
	}
	return uuid
}

func receiveSlabOf(t *testing.T, pool *pgxpool.Pool, item string) *inventory.Unit {
	t.Helper()
	u, err := inventory.CreateUnit(context.Background(), pool, inventory.CreateUnitInput{
		Serial: uniqStr("SLAB"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("receive slab: %v", err)
	}
	return u
}

func seedCustomerUUID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var custType int
	if err := pool.QueryRow(context.Background(),
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custType); err != nil {
		t.Fatalf("resolve CUST: %v", err)
	}
	var uuid string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`, custType, uniqStr("Stone Customer")).Scan(&uuid); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return uuid
}

// createOrder saves a sales order from a JSON items array (CreateOrderInput's
// header fields are unexported, so the payload goes in the way a client sends it).
func createOrder(t *testing.T, pool *pgxpool.Pool, itemsJSON string) *salesorder.Order {
	t.Helper()
	var in salesorder.CreateOrderInput
	body := fmt.Sprintf(`{"customerUuid":%q,"items":%s}`, seedCustomerUUID(t, pool), itemsJSON)
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatalf("build order: %v", err)
	}
	o, err := salesorder.Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	// A job can only be opened from a confirmed order; salesorder.Create starts
	// it as a Draft.
	setSalesOrderStatus(t, pool, o.ID, "OPEN")
	return o
}

// stoneJob is a fabrication job for an order of slab material.
type stoneJob struct {
	pool   *pgxpool.Pool
	item   string
	order  *salesorder.Order
	job    *Job
	slabs  []*inventory.Unit // 3000 x 1400 mm each
	lineID string
}

// newStoneJob orders `ordered` sq ft of a fresh material, stocks slabCount slabs
// of it and opens a job whose blueprint is one pieceL x pieceW mm piece linked to
// the order line (no piece at all when pieceL is 0).
func newStoneJob(t *testing.T, ordered float64, slabCount int, pieceL, pieceW float64) *stoneJob {
	t.Helper()
	pool := testPool(t)
	sj := &stoneJob{pool: pool, item: seedSlabItem(t, pool)}
	for i := 0; i < slabCount; i++ {
		sj.slabs = append(sj.slabs, receiveSlabOf(t, pool, sj.item))
	}
	sj.order = createOrder(t, pool,
		fmt.Sprintf(`[{"lineNumber":1,"inventoryItemUuid":%q,"quantity":%v}]`, sj.item, ordered))
	sj.lineID = sj.order.Items[0].ID

	in := CreateJobInput{SalesOrderUUID: sj.order.ID}
	if pieceL > 0 {
		in.Pieces = []PieceInput{{
			PieceName: "Island", LengthMM: pieceL, WidthMM: pieceW, ThicknessMM: 30, SalesOrderItemUUID: sj.lineID,
		}}
	}
	job, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	sj.job = job
	return sj
}

// setStatus puts the job straight into a status, so a test about cutting does not
// have to walk the approval gates that lead to it.
func (sj *stoneJob) setStatus(t *testing.T, code string) {
	t.Helper()
	if _, err := sj.pool.Exec(context.Background(), `
		UPDATE fabrication_job SET fabrication_job_status = (
			SELECT rs.record_status_id FROM lkp_record_status rs
			JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
			WHERE rt.record_type_code = 'FJOB' AND rs.record_status_code = $2)
		WHERE fabrication_job_uuid = $1`, sj.job.ID, code); err != nil {
		t.Fatalf("set job status: %v", err)
	}
}

func (sj *stoneJob) materials(t *testing.T) []Material {
	t.Helper()
	m, err := LoadMaterials(context.Background(), sj.pool, sj.job.ID)
	if err != nil {
		t.Fatalf("LoadMaterials: %v", err)
	}
	return m
}

func (sj *stoneJob) allocate(t *testing.T, slab *inventory.Unit) {
	t.Helper()
	if err := AllocateSlab(context.Background(), sj.pool, sj.job.ID, slab.ID, "", 1); err != nil {
		t.Fatalf("AllocateSlab: %v", err)
	}
}

func pieceArea(t *testing.T, l, w float64) float64 {
	t.Helper()
	a, err := inventory.AreaFor(l, w, "SQFT", "area")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// ---- materials ----------------------------------------------------------------

func TestMaterials_NeededComesFromTheBlueprint(t *testing.T) {
	sj := newStoneJob(t, 60, 2, 3000, 1000)

	mats := sj.materials(t)

	if len(mats) != 1 {
		t.Fatalf("materials = %+v, want one", mats)
	}
	m := mats[0]
	want := pieceArea(t, 3000, 1000)
	if m.ItemID != sj.item || m.UnitCode != "SQFT" {
		t.Errorf("material = %+v, want item %s in SQFT", m, sj.item)
	}
	if m.Ordered != 60 || m.Needed != want || m.Basis != BasisBlueprint || m.PieceCount != 1 {
		t.Errorf("ordered/needed/basis/pieces = %v/%v/%s/%d, want 60/%v/blueprint/1", m.Ordered, m.Needed, m.Basis, m.PieceCount, want)
	}
	if m.Allocated != 0 || m.Shortfall != want {
		t.Errorf("allocated %v shortfall %v, want 0 and the whole %v", m.Allocated, m.Shortfall, want)
	}
	if m.InStock != roundedSlabs(2) {
		t.Errorf("in stock = %v, want both slabs (%v)", m.InStock, roundedSlabs(2))
	}
}

func roundedSlabs(n int) float64 { return float64(n) * slabSqFt }

func TestMaterials_FallBackToTheOrderWhenThereAreNoPiecesYet(t *testing.T) {
	sj := newStoneJob(t, 60, 2, 0, 0)

	m := sj.materials(t)[0]

	if m.Basis != BasisOrder || m.Needed != 60 || m.PieceCount != 0 {
		t.Errorf("basis/needed/pieces = %s/%v/%d, want order/60/0", m.Basis, m.Needed, m.PieceCount)
	}
}

func TestMaterials_AllocationCountsTowardsCoverage(t *testing.T) {
	sj := newStoneJob(t, 60, 2, 3000, 1000)
	sj.setStatus(t, StatusFabricationReady)
	sj.allocate(t, sj.slabs[0])

	m := sj.materials(t)[0]

	if m.Allocated != slabSqFt || m.Shortfall != 0 {
		t.Errorf("allocated %v shortfall %v, want %v and 0 (one slab covers the piece)", m.Allocated, m.Shortfall, slabSqFt)
	}
	// The slab held for this job is no longer free for another.
	if m.InStock != slabSqFt {
		t.Errorf("in stock = %v, want just the other slab (%v)", m.InStock, slabSqFt)
	}
}

// ---- the Cutting gate ------------------------------------------------------------

func TestTransition_CuttingIsRefusedUntilTheMaterialIsCovered(t *testing.T) {
	sj := newStoneJob(t, 60, 3, 5000, 2000) // 107.6 sq ft: needs three slabs
	sj.setStatus(t, StatusFabricationReady)
	ctx := context.Background()
	need := pieceArea(t, 5000, 2000)

	_, err := Transition(ctx, sj.pool, sj.job.ID, StatusCutting, 1)
	if !IsClientError(err) || !strings.Contains(err.Error(), "Cutting cannot start") {
		t.Fatalf("cutting with nothing allocated = %v, want the material refusal", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("needs %s SQFT but only 0 is allocated", formatQty(need))) {
		t.Errorf("refusal %q must say how much is needed and allocated", err.Error())
	}

	// Two slabs (90.4) is still short of 107.6.
	sj.allocate(t, sj.slabs[0])
	sj.allocate(t, sj.slabs[1])
	if _, err := Transition(ctx, sj.pool, sj.job.ID, StatusCutting, 1); !IsClientError(err) {
		t.Fatalf("cutting with two of three slabs = %v, want refused", err)
	}
	// Refused means nothing was consumed.
	for _, s := range sj.slabs[:2] {
		if got := slabStatus(t, sj.pool, s.ID); got != "reserved" {
			t.Errorf("slab %s is %s after a refused cut, want reserved", s.Serial, got)
		}
	}

	sj.allocate(t, sj.slabs[2])
	if _, err := Transition(ctx, sj.pool, sj.job.ID, StatusCutting, 1); err != nil {
		t.Fatalf("cutting with enough material: %v", err)
	}
	for _, s := range sj.slabs {
		if got := slabStatus(t, sj.pool, s.ID); got != "consumed" {
			t.Errorf("slab %s is %s, want consumed by the cut", s.Serial, got)
		}
	}
	if m := sj.materials(t)[0]; m.Consumed != roundArea(3*slabSqFt) || m.Shortfall != 0 {
		t.Errorf("after cutting: consumed %v shortfall %v, want %v and 0", m.Consumed, m.Shortfall, roundArea(3*slabSqFt))
	}
}

func TestTransition_CuttingAJobWithNoMaterialToCheckIsNotBlocked(t *testing.T) {
	pool := testPool(t)
	// An order of free-text lines only: nothing slab-tracked, so nothing to cover.
	order := createOrder(t, pool, `[{"lineNumber":1,"description":"Backsplash install","quantity":1}]`)
	job, err := Create(context.Background(), pool, CreateJobInput{SalesOrderUUID: order.ID}, 1)
	if err != nil {
		t.Fatal(err)
	}
	sj := &stoneJob{pool: pool, job: job}
	sj.setStatus(t, StatusFabricationReady)

	if _, err := Transition(context.Background(), pool, job.ID, StatusCutting, 1); err != nil {
		t.Fatalf("cutting: %v", err)
	}
}

func slabStatus(t *testing.T, pool *pgxpool.Pool, slabUUID string) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(),
		`SELECT slab_status FROM inventory_slab WHERE inventory_slab_uuid = $1`, slabUUID).Scan(&s); err != nil {
		t.Fatalf("read slab status: %v", err)
	}
	return s
}

// ---- allocating ---------------------------------------------------------------------

func TestAllocateSlab_RefusesAnotherMaterial(t *testing.T) {
	sj := newStoneJob(t, 40, 1, 3000, 1000)
	sj.setStatus(t, StatusMaterialAllocated)
	other := receiveSlabOf(t, sj.pool, seedSlabItem(t, sj.pool))

	err := AllocateSlab(context.Background(), sj.pool, sj.job.ID, other.ID, "", 1)

	if !IsClientError(err) || !strings.Contains(err.Error(), "different material") {
		t.Fatalf("allocating another material's slab = %v, want a 'different material' refusal", err)
	}
	if got := slabStatus(t, sj.pool, other.ID); got != "available" {
		t.Errorf("the refused slab is %s, want it untouched (available)", got)
	}
}

func TestAllocateSlab_AnySlabWhenTheOrderNamesNoCatalogueStone(t *testing.T) {
	pool := testPool(t)
	order := createOrder(t, pool, `[{"lineNumber":1,"description":"Granite countertop","quantity":30}]`)
	job, err := Create(context.Background(), pool, CreateJobInput{SalesOrderUUID: order.ID}, 1)
	if err != nil {
		t.Fatal(err)
	}
	sj := &stoneJob{pool: pool, job: job}
	sj.setStatus(t, StatusMaterialAllocated)
	slab := receiveSlabOf(t, pool, seedSlabItem(t, pool))

	if err := AllocateSlab(context.Background(), pool, job.ID, slab.ID, "", 1); err != nil {
		t.Fatalf("allocating with no material named on the order: %v", err)
	}
}

// ---- consumption and the order's reservation -----------------------------------------

func TestCutting_ShrinksTheOrdersHold_SoTheStoneIsNotCountedTwice(t *testing.T) {
	sj := newStoneJob(t, 40, 2, 3000, 1000)
	ctx := context.Background()
	// 90.4 on hand, 40 held by this order: 50.4 is free for anyone else.
	sj.setStatus(t, StatusFabricationReady)
	sj.allocate(t, sj.slabs[0])

	if _, err := Transition(ctx, sj.pool, sj.job.ID, StatusCutting, 1); err != nil {
		t.Fatalf("cut: %v", err)
	}

	// One slab (45.2) left stock and the order's 40 is covered by it, so the
	// hold is gone and the remaining slab (45.2) is entirely free.
	var status string
	var fulfilled, held float64
	if err := sj.pool.QueryRow(ctx, `
		SELECT a.allocation_status, a.fulfilled_quantity, a.allocated_quantity - a.fulfilled_quantity
		FROM inventory_allocation a JOIN sales_order so ON so.sales_order_id = a.sales_order_id
		WHERE so.sales_order_uuid = $1`, sj.order.ID).Scan(&status, &fulfilled, &held); err != nil {
		t.Fatal(err)
	}
	if status != "fulfilled" || fulfilled != 40 || held != 0 {
		t.Errorf("reservation = %s fulfilled %v held %v, want fulfilled 40 held 0", status, fulfilled, held)
	}
	back, err := salesorder.Get(ctx, sj.pool, sj.order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Items[0].FulfilledQuantity != 40 {
		t.Errorf("line fulfilled = %v, want 40 (the area cut, not a flat 1)", back.Items[0].FulfilledQuantity)
	}
	// Proof it is not double counted: another order can take the whole slab left.
	createOrder(t, sj.pool, fmt.Sprintf(`[{"lineNumber":1,"inventoryItemUuid":%q,"quantity":45}]`, sj.item))
}

func TestBumpFulfillment_LeavesAReservedLineToTheStoneActuallyCut(t *testing.T) {
	sj := newStoneJob(t, 40, 1, 3000, 1000)
	ctx := context.Background()
	var jobID int
	if err := sj.pool.QueryRow(ctx, `SELECT fabrication_job_id FROM fabrication_job WHERE fabrication_job_uuid = $1`, sj.job.ID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	fulfilledOf := func() float64 {
		var v float64
		if err := sj.pool.QueryRow(ctx, `SELECT line_fulfilled_quantity FROM sales_order_item WHERE sales_order_item_uuid = $1`, sj.lineID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	bump := func() {
		tx, err := sj.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := bumpFulfillment(ctx, tx, jobID, 1); err != nil {
			t.Fatalf("bumpFulfillment: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}

	bump()
	if got := fulfilledOf(); got != 0 {
		t.Errorf("a reserved line was bumped to %v, want 0: area is tracked by consumption", got)
	}

	// An order that predates reservations keeps the old per-piece count.
	if _, err := sj.pool.Exec(ctx, `
		DELETE FROM inventory_allocation WHERE sales_order_item_id =
			(SELECT sales_order_item_id FROM sales_order_item WHERE sales_order_item_uuid = $1)`, sj.lineID); err != nil {
		t.Fatal(err)
	}
	bump()
	if got := fulfilledOf(); got != 1 {
		t.Errorf("an unreserved line = %v, want 1 from the legacy per-piece count", got)
	}
}
