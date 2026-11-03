//go:build dbtest

package replenishment

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/dbtestutil"
	"stonesuite-backend/purchaseorder"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	dbtestutil.EnsureLocation(t, pool)
	return pool
}

func uniq() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }

func seedVendor(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var uuid string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		SELECT t.record_type_id, s.record_status_id, 'Organization', $1, 1
		FROM lkp_record_type t
		JOIN lkp_record_status s ON s.record_status_record_type = t.record_type_id AND s.record_status_code = 'ACT_'
		WHERE t.record_type_code = 'VNDR'
		RETURNING vendor_uuid`, "Reorder Vendor "+uniq()).Scan(&uuid))
	return uuid
}

// seedItem inserts a stock-tracked SQFT item; tracking is 'serialized' (slabs)
// or 'quantity' (bulk).
func seedItem(t *testing.T, pool *pgxpool.Pool, tracking string, lengthMM, widthMM float64) (uuid string, id int) {
	t.Helper()
	suffix := uniq()
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id,
			inventory_item_unit_price, inventory_item_purchase_unit_price, inventory_item_tracking,
			inventory_item_length_mm, inventory_item_width_mm, inventory_item_thickness_mm,
			inventory_item_dimension_unit, inventory_item_created_by)
		VALUES ($1, $2, (SELECT unit_id FROM lkp_unit WHERE unit_code = 'SQFT'), 40, 25, $3, $4, $5, 20, 'in', 1)
		RETURNING inventory_item_uuid, inventory_item_id`,
		"RO-"+suffix, "Reorder Item "+suffix, tracking, lengthMM, widthMM).Scan(&uuid, &id))
	return uuid, id
}

// seedSlab inserts one 50 sq ft slab of the item. A 'cut' slab hangs off parent.
func seedSlab(t *testing.T, pool *pgxpool.Pool, itemID int, status, form string, parent *int) int {
	t.Helper()
	var id int
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO inventory_slab (slab_serial, inventory_item_id, warehouse_id, slab_length_mm, slab_width_mm,
			slab_thickness_mm, slab_area, slab_area_unit_id, slab_status, slab_form, slab_parent_slab_id)
		VALUES ($1, $2, 1, 3000, 1500, 20, 50, (SELECT unit_id FROM lkp_unit WHERE unit_code = 'SQFT'), $3, $4, $5)
		RETURNING inventory_slab_id`,
		"RO-S-"+uniq(), itemID, status, form, parent).Scan(&id))
	return id
}

// consumeSlab records a slab as consumed on the given day.
func consumeSlab(t *testing.T, pool *pgxpool.Pool, itemID int, form string, parent *int, at time.Time) {
	t.Helper()
	slab := seedSlab(t, pool, itemID, "consumed", form, parent)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO inventory_slab_ledger (inventory_slab_id, inventory_item_id, warehouse_id, event, quantity_delta, occurred_at)
		VALUES ($1, $2, 1, 'consumed', -50, $3)`, slab, itemID, at)
	require.NoError(t, err)
}

func bulkUsage(t *testing.T, pool *pgxpool.Pool, itemID int, qty float64, at time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO inventory_ledger (inventory_item_id, warehouse_id, event, quantity_delta, occurred_at)
		VALUES ($1, 1, 'consumed', $2, $3)`, itemID, -qty, at)
	require.NoError(t, err)
}

func findItem(items []Item, uuid string) *Item {
	for i := range items {
		if items[i].InventoryItemID == uuid {
			return &items[i]
		}
	}
	return nil
}

// monthDay is the 10th of the month `back` months before now's.
func monthDay(now time.Time, back int) time.Time {
	return time.Date(now.Year(), now.Month(), 10, 12, 0, 0, 0, time.UTC).AddDate(0, -back, 0)
}

func TestLoadItems_SlabItemCountsFullSlabsOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// An unsized item: the standard size is not stated, so a draft falls back to
	// slabs x average area.
	itemUUID, itemID := seedItem(t, pool, "serialized", 0, 0)

	// Slabs used in the three history months: 5, 4 and 6.
	for month, n := range map[int]int{3: 5, 2: 4, 1: 6} {
		for i := 0; i < n; i++ {
			consumeSlab(t, pool, itemID, "full", nil, monthDay(now, month))
		}
	}
	// Not history: this month, and four months ago.
	consumeSlab(t, pool, itemID, "full", nil, monthDay(now, 0))
	consumeSlab(t, pool, itemID, "full", nil, monthDay(now, 4))
	// A remnant used up last month is the same stone as the slab it was cut from.
	parent := seedSlab(t, pool, itemID, "consumed", "full", nil)
	consumeSlab(t, pool, itemID, "cut", &parent, monthDay(now, 1))

	// On hand: 2 available + 1 reserved full slabs. Not counted: a remnant, a
	// scrapped slab, and one that is only on order.
	seedSlab(t, pool, itemID, "available", "full", nil)
	seedSlab(t, pool, itemID, "available", "full", nil)
	seedSlab(t, pool, itemID, "reserved", "full", nil)
	seedSlab(t, pool, itemID, "available", "cut", &parent)
	seedSlab(t, pool, itemID, "scrapped", "full", nil)
	seedSlab(t, pool, itemID, "on_order", "full", nil)

	res, err := LoadItems(ctx, pool, now)
	require.NoError(t, err)
	assert.Len(t, res.Months, HistoryMonths)

	got := findItem(res.Items, itemUUID)
	require.NotNil(t, got, "a short item is listed")
	assert.Equal(t, SlabUnit, got.Unit)
	assert.Equal(t, []float64{5, 4, 6}, []float64{got.Usage[0].Used, got.Usage[1].Used, got.Usage[2].Used})
	assert.Equal(t, 5.0, got.MonthlyRequirement)
	assert.Equal(t, 3.0, got.OnHand)
	assert.Equal(t, 0.0, got.OnOrder)
	assert.Equal(t, 2.0, got.Needed)

	// Ordering 1 slab leaves 1 still short, and the draft is listed as on order.
	vendor := seedVendor(t, pool)
	drafts, err := CreateDrafts(ctx, pool, DraftInput{
		InventoryItemID: itemUUID, Vendors: []VendorQty{{VendorID: vendor, Qty: 1}},
	}, 1)
	require.NoError(t, err)
	require.Len(t, drafts, 1)

	res, err = LoadItems(ctx, pool, now)
	require.NoError(t, err)
	got = findItem(res.Items, itemUUID)
	require.NotNil(t, got)
	assert.Equal(t, 1.0, got.OnOrder)
	assert.Equal(t, 1.0, got.Needed)
	require.Len(t, got.OpenOrders, 1)
	assert.Equal(t, drafts[0].PurchaseOrderID, got.OpenOrders[0].ID)
	assert.Equal(t, 1.0, got.OpenOrders[0].Remaining)

	// A second slab covers the rest: the item drops off the list.
	_, err = CreateDrafts(ctx, pool, DraftInput{
		InventoryItemID: itemUUID, Vendors: []VendorQty{{VendorID: seedVendor(t, pool), Qty: 1}},
	}, 1)
	require.NoError(t, err)
	res, err = LoadItems(ctx, pool, now)
	require.NoError(t, err)
	assert.Nil(t, findItem(res.Items, itemUUID), "a covered item is not listed")
}

func TestLoadItems_BulkItemSplitAcrossVendors(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	itemUUID, itemID := seedItem(t, pool, "quantity", 0, 0)
	for month := 1; month <= 3; month++ {
		bulkUsage(t, pool, itemID, 10, monthDay(now, month))
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand) VALUES ($1, 1, 4)`, itemID)
	require.NoError(t, err)

	res, err := LoadItems(ctx, pool, now)
	require.NoError(t, err)
	got := findItem(res.Items, itemUUID)
	require.NotNil(t, got)
	assert.Equal(t, "SQFT", got.Unit)
	assert.Equal(t, 10.0, got.MonthlyRequirement)
	assert.Equal(t, 6.0, got.Needed)

	vendorA, vendorB := seedVendor(t, pool), seedVendor(t, pool)
	drafts, err := CreateDrafts(ctx, pool, DraftInput{
		InventoryItemID: itemUUID,
		Vendors:         []VendorQty{{VendorID: vendorA, Qty: 4}, {VendorID: vendorB, Qty: 2}},
	}, 1)
	require.NoError(t, err)
	require.Len(t, drafts, 2)
	assert.NotEqual(t, drafts[0].PurchaseOrderID, drafts[1].PurchaseOrderID, "one PO per vendor")

	// The two drafts together cover the whole gap.
	res, err = LoadItems(ctx, pool, now)
	require.NoError(t, err)
	assert.Nil(t, findItem(res.Items, itemUUID))

	for i, want := range []float64{4, 2} {
		po, err := purchaseorder.Get(ctx, pool, drafts[i].PurchaseOrderID)
		require.NoError(t, err)
		require.Len(t, po.Items, 1)
		assert.Equal(t, want, po.Items[0].Quantity)
		assert.Equal(t, 25.0, po.Items[0].UnitPrice, "priced at the purchasing price")
	}
}

func TestCreateDrafts_SizedSlabItemCarriesItsSize(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	itemUUID, _ := seedItem(t, pool, "serialized", 3000, 1500)

	drafts, err := CreateDrafts(ctx, pool, DraftInput{
		InventoryItemID: itemUUID, Vendors: []VendorQty{{VendorID: seedVendor(t, pool), Qty: 3}},
	}, 1)
	require.NoError(t, err)

	po, err := purchaseorder.Get(ctx, pool, drafts[0].PurchaseOrderID)
	require.NoError(t, err)
	require.Len(t, po.Items, 1)
	line := po.Items[0]
	require.NotNil(t, line.ExpectedSlabs)
	assert.Equal(t, 3, *line.ExpectedSlabs)
	require.NotNil(t, line.Length)
	assert.Equal(t, 118.11, *line.Length)
	assert.Equal(t, "in", line.DimensionUnit)
	// 3 slabs x 118.11 in x 59.06 in, in sq ft.
	assert.InDelta(t, 3*118.11*59.06/144, line.Quantity, 0.05)
}

func TestCreateDrafts_IsAllOrNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	itemUUID, _ := seedItem(t, pool, "quantity", 0, 0)
	good := seedVendor(t, pool)

	countFor := func() (n int) {
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM purchase_order po JOIN vendor v ON v.vendor_id = po.purchase_order_vendor_id
			WHERE v.vendor_uuid = $1`, good).Scan(&n))
		return n
	}

	_, err := CreateDrafts(ctx, pool, DraftInput{
		InventoryItemID: itemUUID,
		Vendors:         []VendorQty{{VendorID: good, Qty: 1}, {VendorID: "00000000-0000-0000-0000-000000000000", Qty: 1}},
	}, 1)
	require.Error(t, err)
	assert.True(t, purchaseorder.IsClientError(err), "an unknown vendor is the caller's to fix")
	assert.Zero(t, countFor(), "the first vendor's PO was rolled back")
}

func TestCreateDrafts_RefusesAnItemThatCannotBeReordered(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)
	in := func(item string) DraftInput {
		return DraftInput{InventoryItemID: item, Vendors: []VendorQty{{VendorID: vendor, Qty: 1}}}
	}

	_, err := CreateDrafts(ctx, pool, in("00000000-0000-0000-0000-000000000000"), 1)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = CreateDrafts(ctx, pool, in("not-a-uuid"), 1)
	assert.ErrorIs(t, err, ErrNotFound, "a malformed id is not found, not a database error")

	untracked, id := seedItem(t, pool, "quantity", 0, 0)
	_, err = pool.Exec(ctx, `UPDATE inventory_item SET inventory_item_track_stock = FALSE WHERE inventory_item_id = $1`, id)
	require.NoError(t, err)
	_, err = CreateDrafts(ctx, pool, in(untracked), 1)
	assert.ErrorIs(t, err, ErrNotFound, "an item that does not track stock has no reorder")
}
