//go:build dbtest

package replenishment

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/purchaseorder"
)

// setPOStatus moves a purchase order to the given status code (SENT, CLSD, ...).
func setPOStatus(t *testing.T, pool *pgxpool.Pool, poUUID, code string) {
	t.Helper()
	tag, err := pool.Exec(context.Background(), `
		UPDATE purchase_order SET purchase_order_status = (
			SELECT s.record_status_id FROM lkp_record_status s
			JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type
			WHERE t.record_type_code = 'PORD' AND s.record_status_code = $2)
		WHERE purchase_order_uuid = $1`, poUUID, code)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
}

// poLineIDs returns the internal ids of a purchase order's lines.
func poLineIDs(t *testing.T, pool *pgxpool.Pool, poUUID string) []int {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT poi.purchase_order_item_id FROM purchase_order_item poi
		JOIN purchase_order po ON po.purchase_order_id = poi.purchase_order_id
		WHERE po.purchase_order_uuid = $1 ORDER BY poi.line_number`, poUUID)
	require.NoError(t, err)
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// mintOnOrder inserts n On Order slabs tied to a PO line, as sending the order does.
func mintOnOrder(t *testing.T, pool *pgxpool.Pool, itemID, lineID, n int) []int {
	t.Helper()
	var ids []int
	for i := 0; i < n; i++ {
		var id int
		require.NoError(t, pool.QueryRow(context.Background(), `
			INSERT INTO inventory_slab (slab_serial, inventory_item_id, warehouse_id, slab_length_mm, slab_width_mm,
				slab_thickness_mm, slab_area, slab_area_unit_id, slab_status, slab_form, purchase_order_item_id)
			VALUES ($1, $2, 1, 3000, 1500, 20, 48.44, (SELECT unit_id FROM lkp_unit WHERE unit_code = 'SQFT'),
				'on_order', 'full', $3) RETURNING inventory_slab_id`, "RO-OO-"+uniq(), itemID, lineID).Scan(&id))
		ids = append(ids, id)
	}
	return ids
}

func setSlabStatus(t *testing.T, pool *pgxpool.Pool, slabID int, status string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `UPDATE inventory_slab SET slab_status = $2 WHERE inventory_slab_id = $1`, slabID, status)
	require.NoError(t, err)
}

// useSlabs consumes n full slabs of the item in each of the three history months
// before `now`, so the monthly requirement is exactly n.
func useSlabs(t *testing.T, pool *pgxpool.Pool, itemID int, now time.Time, n int) {
	t.Helper()
	for month := 1; month <= HistoryMonths; month++ {
		for i := 0; i < n; i++ {
			consumeSlab(t, pool, itemID, "full", nil, monthDay(now, month))
		}
	}
}

// A sent order's slabs are the truth for what is still on order: receiving one
// moves it from "on order" to "on hand" without double counting, and cancelling
// the order takes the rest off again.
func TestScenario_SentOrderThroughReceiptAndCancel(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	itemUUID, itemID := seedItem(t, pool, "serialized", 3000, 1500)
	useSlabs(t, pool, itemID, now, 4) // requirement 4

	drafts, err := CreateDrafts(ctx, pool, DraftInput{
		InventoryItemID: itemUUID, Vendors: []VendorQty{{VendorID: seedVendor(t, pool), Qty: 2}},
	}, 1)
	require.NoError(t, err)
	po := drafts[0].PurchaseOrderID

	want := func(label string, onHand, onOrder, needed float64) {
		t.Helper()
		res, err := LoadItems(ctx, pool, now)
		require.NoError(t, err)
		got := findItem(res.Items, itemUUID)
		require.NotNil(t, got, label)
		assert.Equal(t, onHand, got.OnHand, label+": on hand")
		assert.Equal(t, onOrder, got.OnOrder, label+": on order")
		assert.Equal(t, needed, got.Needed, label+": needed")
	}

	want("draft", 0, 2, 2)

	// Sent: two On Order slabs exist for the line.
	setPOStatus(t, pool, po, "SENT")
	minted := mintOnOrder(t, pool, itemID, poLineIDs(t, pool, po)[0], 2)
	want("sent", 0, 2, 2)

	// One arrives: it moves from on order to on hand, the gap is unchanged.
	setSlabStatus(t, pool, minted[0], "available")
	want("one received", 1, 1, 2)

	// The order is cancelled with one slab still to come: that one is gone.
	setPOStatus(t, pool, po, "CANC")
	want("cancelled", 1, 0, 3)
}

func TestScenario_ClosedOrderDoesNotCount(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	itemUUID, itemID := seedItem(t, pool, "serialized", 3000, 1500)
	useSlabs(t, pool, itemID, now, 4)

	drafts, err := CreateDrafts(ctx, pool, DraftInput{
		InventoryItemID: itemUUID, Vendors: []VendorQty{{VendorID: seedVendor(t, pool), Qty: 4}},
	}, 1)
	require.NoError(t, err)

	res, err := LoadItems(ctx, pool, now)
	require.NoError(t, err)
	assert.Nil(t, findItem(res.Items, itemUUID), "a draft that covers the need hides the item")

	setPOStatus(t, pool, drafts[0].PurchaseOrderID, "CLSD")
	res, err = LoadItems(ctx, pool, now)
	require.NoError(t, err)
	got := findItem(res.Items, itemUUID)
	require.NotNil(t, got, "a closed order brings nothing more, so the item is short again")
	assert.Equal(t, 4.0, got.Needed)
	assert.Empty(t, got.OpenOrders)
}

// An unsized line has no minted slabs, and slabs received against it are not
// linked back to it, so a part-received line keeps only its matching share.
func TestScenario_PartlyReceivedUnsizedLine(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	vendor := seedVendor(t, pool)
	itemUUID, itemID := seedItem(t, pool, "serialized", 0, 0)
	useSlabs(t, pool, itemID, now, 6) // requirement 6

	five := 5
	in := purchaseorder.CreatePurchaseOrderInput{VendorUUID: vendor}
	in.Items = []purchaseorder.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 250, ExpectedSlabs: &five}}
	po, err := purchaseorder.Create(ctx, pool, in, 1)
	require.NoError(t, err)

	res, err := LoadItems(ctx, pool, now)
	require.NoError(t, err)
	got := findItem(res.Items, itemUUID)
	require.NotNil(t, got)
	assert.Equal(t, 5.0, got.OnOrder)
	assert.Equal(t, 1.0, got.Needed)

	// 100 of 250 sq ft arrives (2 slabs' worth): 3 slabs are still to come.
	_, err = pool.Exec(ctx, `UPDATE purchase_order_item SET qty_received = 100 WHERE purchase_order_item_id = $1`,
		poLineIDs(t, pool, po.ID)[0])
	require.NoError(t, err)
	res, err = LoadItems(ctx, pool, now)
	require.NoError(t, err)
	got = findItem(res.Items, itemUUID)
	require.NotNil(t, got)
	assert.Equal(t, 3.0, got.OnOrder)
	assert.Equal(t, 3.0, got.Needed)
}

// Two lines of one item on one PO are one entry in the open-orders list, and
// separate POs are separate entries.
func TestScenario_OpenOrdersAreGroupedPerPO(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	vendor := seedVendor(t, pool)
	itemUUID, itemID := seedItem(t, pool, "quantity", 0, 0)
	for month := 1; month <= HistoryMonths; month++ {
		bulkUsage(t, pool, itemID, 30, monthDay(now, month))
	}

	two := purchaseorder.CreatePurchaseOrderInput{VendorUUID: vendor}
	two.Items = []purchaseorder.LineInput{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 4},
		{LineNumber: 2, InventoryItemUUID: itemUUID, Quantity: 6},
	}
	first, err := purchaseorder.Create(ctx, pool, two, 1)
	require.NoError(t, err)
	one := purchaseorder.CreatePurchaseOrderInput{VendorUUID: vendor}
	one.Items = []purchaseorder.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 5}}
	second, err := purchaseorder.Create(ctx, pool, one, 1)
	require.NoError(t, err)

	res, err := LoadItems(ctx, pool, now)
	require.NoError(t, err)
	got := findItem(res.Items, itemUUID)
	require.NotNil(t, got)
	assert.Equal(t, 15.0, got.OnOrder)
	assert.Equal(t, 15.0, got.Needed)
	require.Len(t, got.OpenOrders, 2)
	byID := map[string]float64{}
	for _, o := range got.OpenOrders {
		byID[o.ID] = o.Remaining
		assert.Equal(t, "Draft", o.Status)
	}
	assert.Equal(t, 10.0, byID[first.ID])
	assert.Equal(t, 5.0, byID[second.ID])

	// A partly received bulk line counts only what is left.
	_, err = pool.Exec(ctx, `UPDATE purchase_order_item SET qty_received = 3 WHERE purchase_order_item_id = $1`,
		poLineIDs(t, pool, second.ID)[0])
	require.NoError(t, err)
	res, err = LoadItems(ctx, pool, now)
	require.NoError(t, err)
	assert.Equal(t, 12.0, findItem(res.Items, itemUUID).OnOrder)
}

// The window is the three full calendar months before the current one, to the
// second at both ends.
func TestScenario_WindowBoundaries(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, time.November, 15, 10, 0, 0, 0, time.UTC)
	itemUUID, itemID := seedItem(t, pool, "serialized", 3000, 1500)

	at := func(y int, m time.Month, d, h, mi, s int) time.Time { return time.Date(y, m, d, h, mi, s, 0, time.UTC) }
	// Counted: first instant of Aug, last second of Oct.
	consumeSlab(t, pool, itemID, "full", nil, at(2026, time.August, 1, 0, 0, 0))
	consumeSlab(t, pool, itemID, "full", nil, at(2026, time.October, 31, 23, 59, 59))
	// Not counted: one second before Aug, first instant of Nov, and later in Nov.
	consumeSlab(t, pool, itemID, "full", nil, at(2026, time.July, 31, 23, 59, 59))
	consumeSlab(t, pool, itemID, "full", nil, at(2026, time.November, 1, 0, 0, 0))
	consumeSlab(t, pool, itemID, "full", nil, at(2026, time.November, 14, 9, 0, 0))

	res, err := LoadItems(ctx, pool, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-08", "2026-09", "2026-10"}, res.Months)
	got := findItem(res.Items, itemUUID)
	require.NotNil(t, got)
	assert.Equal(t, []MonthUsage{{"2026-08", 1}, {"2026-09", 0}, {"2026-10", 1}}, got.Usage)
	assert.Equal(t, 1.0, got.MonthlyRequirement, "2 slabs over 3 months is 0.67, rounded up to 1")
}

func TestScenario_ItemsThatAreNeverListed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	inactive, inactiveID := seedItem(t, pool, "serialized", 3000, 1500)
	useSlabs(t, pool, inactiveID, now, 2)
	_, err := pool.Exec(ctx, `UPDATE inventory_item SET inventory_item_is_active = FALSE WHERE inventory_item_id = $1`, inactiveID)
	require.NoError(t, err)

	untracked, untrackedID := seedItem(t, pool, "quantity", 0, 0)
	bulkUsage(t, pool, untrackedID, 9, monthDay(now, 1))
	_, err = pool.Exec(ctx, `UPDATE inventory_item SET inventory_item_track_stock = FALSE WHERE inventory_item_id = $1`, untrackedID)
	require.NoError(t, err)

	unused, _ := seedItem(t, pool, "serialized", 3000, 1500) // never used, no stock

	covered, coveredID := seedItem(t, pool, "serialized", 3000, 1500)
	useSlabs(t, pool, coveredID, now, 2) // requirement 2
	seedSlab(t, pool, coveredID, "available", "full", nil)
	seedSlab(t, pool, coveredID, "available", "full", nil)

	res, err := LoadItems(ctx, pool, now)
	require.NoError(t, err)
	for name, uuid := range map[string]string{"inactive": inactive, "not tracking stock": untracked, "never used": unused, "covered by stock": covered} {
		assert.Nil(t, findItem(res.Items, uuid), name)
	}
}

// Only full slabs that are physically ours count as on hand, and only
// consumption events count as usage.
func TestScenario_SlabStatesOnHand(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	itemUUID, itemID := seedItem(t, pool, "serialized", 3000, 1500)
	useSlabs(t, pool, itemID, now, 10) // requirement 10

	parent := seedSlab(t, pool, itemID, "consumed", "full", nil)
	for _, st := range []string{"available", "reserved", "received", "in_transit"} {
		seedSlab(t, pool, itemID, st, "full", nil) // counted: 4
	}
	for _, st := range []string{"scrapped", "on_order"} {
		seedSlab(t, pool, itemID, st, "full", nil) // not counted
	}
	seedSlab(t, pool, itemID, "available", "cut", &parent) // a remnant: not counted
	// Scrapping and recovering are not usage.
	_, err := pool.Exec(ctx, `
		INSERT INTO inventory_slab_ledger (inventory_slab_id, inventory_item_id, warehouse_id, event, quantity_delta, occurred_at)
		SELECT inventory_slab_id, inventory_item_id, 1, 'scrapped', -48, $2 FROM inventory_slab WHERE inventory_slab_id = $1`,
		seedSlab(t, pool, itemID, "scrapped", "full", nil), monthDay(now, 1))
	require.NoError(t, err)

	res, err := LoadItems(ctx, pool, now)
	require.NoError(t, err)
	got := findItem(res.Items, itemUUID)
	require.NotNil(t, got)
	assert.Equal(t, 4.0, got.OnHand)
	assert.Equal(t, 10.0, got.MonthlyRequirement)
	assert.Equal(t, 6.0, got.Needed)
}

// A quantity-tracked item sums stock over every location and counts only
// 'consumed' ledger rows as usage.
func TestScenario_BulkItemStockAndUsage(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	itemUUID, itemID := seedItem(t, pool, "quantity", 0, 0)

	var secondLoc int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO company_location (name, is_default) VALUES ($1, FALSE) RETURNING company_location_id`,
		"Reorder Yard "+uniq()).Scan(&secondLoc))
	for _, loc := range []int{1, secondLoc} {
		_, err := pool.Exec(ctx, `INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand) VALUES ($1, $2, 5)`, itemID, loc)
		require.NoError(t, err)
	}
	for month := 1; month <= HistoryMonths; month++ {
		bulkUsage(t, pool, itemID, 12.5, monthDay(now, month))
	}
	// Receipts, adjustments and returns are not usage.
	_, err := pool.Exec(ctx, `
		INSERT INTO inventory_ledger (inventory_item_id, warehouse_id, event, quantity_delta, occurred_at)
		VALUES ($1, 1, 'received', 100, $2), ($1, 1, 'adjusted', -40, $2), ($1, 1, 'returned', -7, $2)`, itemID, monthDay(now, 1))
	require.NoError(t, err)

	res, err := LoadItems(ctx, pool, now)
	require.NoError(t, err)
	got := findItem(res.Items, itemUUID)
	require.NotNil(t, got)
	assert.Equal(t, 10.0, got.OnHand, "stock in both locations")
	assert.Equal(t, 13.0, got.MonthlyRequirement, "12.5 a month rounds up")
	assert.Equal(t, 3.0, got.Needed)
	assert.Equal(t, []MonthUsage{{got.Usage[0].Month, 12.5}, {got.Usage[1].Month, 12.5}, {got.Usage[2].Month, 12.5}}, got.Usage)
}

func TestScenario_FractionalBulkQuantityAndSlabRefusal(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)

	bulk, _ := seedItem(t, pool, "quantity", 0, 0)
	drafts, err := CreateDrafts(ctx, pool, DraftInput{InventoryItemID: bulk, Vendors: []VendorQty{{VendorID: vendor, Qty: 2.5}}}, 1)
	require.NoError(t, err)
	po, err := purchaseorder.Get(ctx, pool, drafts[0].PurchaseOrderID)
	require.NoError(t, err)
	assert.Equal(t, 2.5, po.Items[0].Quantity)

	slab, _ := seedItem(t, pool, "serialized", 3000, 1500)
	_, err = CreateDrafts(ctx, pool, DraftInput{InventoryItemID: slab, Vendors: []VendorQty{{VendorID: vendor, Qty: 2.5}}}, 1)
	var ce ClientError
	require.ErrorAs(t, err, &ce)
}
