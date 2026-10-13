//go:build dbtest

package purchaseorder

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedAreaItem inserts a quantity-tracked catalog item bought per square foot at
// $25.00 — an area unit, but not a slab-tracked (serialized) item.
func seedAreaItem(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var uuid string
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price,
			inventory_item_purchase_unit_price, inventory_item_created_by)
		VALUES ($1, $2, (SELECT unit_id FROM lkp_unit WHERE unit_code = 'SQFT'), 40.00, 25.00, 1)
		RETURNING inventory_item_uuid`,
		"POAREA-"+suffix, "PO Area Item "+suffix).Scan(&uuid); err != nil {
		t.Fatalf("seed area item: %v", err)
	}
	return uuid
}

func f64(v float64) *float64 { return &v }

// On a purchase order the slab count IS the line's expected slabs, so a sized
// line stores both, derives its quantity from them, and reads them back.
func TestCreate_SizedLineDerivesQuantityAndKeepsExpectedSlabs(t *testing.T) {
	pool := testPool(t)
	vendorUUID, _ := seedVendorAndItem(t, pool)
	itemUUID := seedAreaItem(t, pool)

	in := CreatePurchaseOrderInput{VendorUUID: vendorUUID}
	in.Items = []LineInput{{
		LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1,
		Length: f64(304.8), Width: f64(152.4), ExpectedSlabs: intPtr(4), DimensionUnit: "cm",
	}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	l := got.Items[0]
	if l.Quantity != 200 {
		t.Errorf("Quantity = %v, want 200 (4 slabs x 50 sqft)", l.Quantity)
	}
	if l.LineSubtotal != 5000 {
		t.Errorf("LineSubtotal = %v, want 5000 at $25 / sqft", l.LineSubtotal)
	}
	if l.ExpectedSlabs == nil || *l.ExpectedSlabs != 4 {
		t.Errorf("ExpectedSlabs = %v, want 4", l.ExpectedSlabs)
	}
	if l.Length == nil || *l.Length != 304.8 || l.Width == nil || *l.Width != 152.4 {
		t.Errorf("size = (%v, %v), want (304.8, 152.4)", l.Length, l.Width)
	}
}

// Length without width is half a size, not a size.
func TestCreate_RefusesALoneDimension(t *testing.T) {
	pool := testPool(t)
	vendorUUID, _ := seedVendorAndItem(t, pool)
	itemUUID := seedAreaItem(t, pool)

	in := CreatePurchaseOrderInput{VendorUUID: vendorUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 5, Length: f64(100)}}
	_, err := Create(context.Background(), pool, in, 1)
	if err == nil || !IsClientError(err) || !strings.Contains(err.Error(), "both length and width") {
		t.Fatalf("err = %v, want a lone length refused", err)
	}
}

// A line without a size is exactly as before: its quantity is what was typed.
func TestCreate_UnsizedLineKeepsTheTypedQuantity(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)

	in := CreatePurchaseOrderInput{VendorUUID: vendorUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 7}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	l := got.Items[0]
	if l.Quantity != 7 || l.Length != nil || l.Width != nil {
		t.Errorf("line = qty %v size (%v, %v), want qty 7 and no size", l.Quantity, l.Length, l.Width)
	}
}

// A purchase order line takes the item's unit too, and keeps it with the size.
func TestCreate_SizedLineTakesTheItemsDimensionUnit(t *testing.T) {
	pool := testPool(t)
	vendorUUID, _ := seedVendorAndItem(t, pool)
	var itemUUID string
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price,
			inventory_item_purchase_unit_price, inventory_item_dimension_unit, inventory_item_created_by)
		VALUES ($1, $2, (SELECT unit_id FROM lkp_unit WHERE unit_code = 'SQFT'), 40.00, 25.00, 'in', 1)
		RETURNING inventory_item_uuid`,
		"POIN-"+suffix, "PO Inch Item "+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inch item: %v", err)
	}

	in := CreatePurchaseOrderInput{VendorUUID: vendorUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Length: f64(120), Width: f64(60), ExpectedSlabs: intPtr(2)}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	l := got.Items[0]
	if l.DimensionUnit != "in" || l.Quantity != 100 {
		t.Errorf("unit = %q, qty = %v, want in and 100 (2 slabs x 50 sq ft)", l.DimensionUnit, l.Quantity)
	}
}
