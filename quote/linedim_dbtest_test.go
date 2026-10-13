//go:build dbtest

package quote

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/estimate"
)

// seedAreaItem inserts a catalog item priced per square foot at $10.00.
func seedAreaItem(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var uuid string
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, (SELECT unit_id FROM lkp_unit WHERE unit_code = 'SQFT'), 10.00, 1)
		RETURNING inventory_item_uuid`,
		"AREA-"+suffix, "Area Item "+suffix).Scan(&uuid); err != nil {
		t.Fatalf("seed area item: %v", err)
	}
	return uuid
}

func f64(v float64) *float64 { return &v }
func ip(v int) *int          { return &v }

// 304.8 x 152.4 cm is exactly 10 ft x 5 ft = 50 sqft, so three slabs are 150
// sqft — at $10 / sqft that is $1500.00 whatever quantity the client sent.
func TestCreate_DerivesTheQuantityFromSlabDimensions(t *testing.T) {
	pool := testPool(t)
	custUUID, _ := seedCustomerAndItem(t, pool)
	itemUUID := seedAreaItem(t, pool)

	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{
		LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 999,
		Length: f64(304.8), Width: f64(152.4), Slabs: ip(3), DimensionUnit: "cm",
	}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	l := got.Items[0]
	if l.Quantity != 150 {
		t.Errorf("Quantity = %v, want 150 (the client's 999 must be ignored)", l.Quantity)
	}
	if l.LineSubtotal != 1500 {
		t.Errorf("LineSubtotal = %v, want 1500", l.LineSubtotal)
	}
	if l.Length == nil || *l.Length != 304.8 || l.Width == nil || *l.Width != 152.4 || l.Slabs == nil || *l.Slabs != 3 {
		t.Errorf("dimensions = (%v, %v, %v), want (304.8, 152.4, 3)", l.Length, l.Width, l.Slabs)
	}
}

// A sized line is priced from its dimensions alone: no quantity is required —
// and with no unit anywhere, inches are assumed (120 x 60 in is 50 sq ft).
func TestCreate_SizedLineNeedsNoQuantity(t *testing.T) {
	pool := testPool(t)
	custUUID, _ := seedCustomerAndItem(t, pool)
	itemUUID := seedAreaItem(t, pool)

	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Length: f64(120), Width: f64(60)}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	l := got.Items[0]
	if l.DimensionUnit != "in" {
		t.Errorf("DimensionUnit = %q, want in (the default)", l.DimensionUnit)
	}
	if l.Quantity != 50 {
		t.Errorf("Quantity = %v, want 50 (one slab assumed)", l.Quantity)
	}
	if l.Slabs != nil {
		t.Errorf("Slabs = %d, want it left blank rather than a stored assumption", *l.Slabs)
	}
}

func TestCreate_UnsizedLineIsUnchanged(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	l := got.Items[0]
	if l.Quantity != 2 || l.Length != nil || l.Width != nil || l.Slabs != nil {
		t.Errorf("line = qty %v dims (%v, %v, %v), want qty 2 and no dimensions", l.Quantity, l.Length, l.Width, l.Slabs)
	}
}

// seedCustomerAndItem's item is priced per Each, where "length x width" means
// nothing.
func TestCreate_RefusesDimensionsOnAnEachItem(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, Length: f64(100), Width: f64(50)}}
	_, err := Create(context.Background(), pool, in, 1)
	if err == nil || !IsClientError(err) || !strings.Contains(err.Error(), "Line 1: length, width and slabs can only be used") {
		t.Fatalf("err = %v, want the dimensions refused on an Each item", err)
	}
}

// Estimate → Quote copies each line verbatim, slab dimensions included.
func TestConvertFromEstimate_CarriesSlabDimensions(t *testing.T) {
	pool := testPool(t)
	custUUID, _ := seedCustomerAndItem(t, pool)
	itemUUID := seedAreaItem(t, pool)

	in := estimate.CreateEstimateInput{CustomerUUID: custUUID}
	in.Items = []estimate.LineInput{{
		LineNumber: 1, InventoryItemUUID: itemUUID,
		Length: f64(304.8), Width: f64(152.4), Slabs: ip(2), DimensionUnit: "cm",
	}}
	est, err := estimate.Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("seed estimate: %v", err)
	}
	if l := est.Items[0]; l.Quantity != 100 || l.Slabs == nil || *l.Slabs != 2 {
		t.Fatalf("estimate line = qty %v slabs %v, want 100 / 2", l.Quantity, l.Slabs)
	}

	got, _, err := ConvertFromEstimate(context.Background(), pool, est.ID, 1)
	if err != nil {
		t.Fatalf("ConvertFromEstimate: %v", err)
	}
	l := got.Items[0]
	if l.Quantity != 100 || l.Length == nil || *l.Length != 304.8 || l.Width == nil || *l.Width != 152.4 || l.Slabs == nil || *l.Slabs != 2 {
		t.Errorf("quote line = qty %v dims (%v, %v, %v), want 100 and (304.8, 152.4, 2)", l.Quantity, l.Length, l.Width, l.Slabs)
	}
}

// seedAreaItemIn is seedAreaItem for an item whose slabs are measured in `unit`.
func seedAreaItemIn(t *testing.T, pool *pgxpool.Pool, unit string) string {
	t.Helper()
	var uuid string
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price,
			inventory_item_dimension_unit, inventory_item_created_by)
		VALUES ($1, $2, (SELECT unit_id FROM lkp_unit WHERE unit_code = 'SQFT'), 10.00, $3, 1)
		RETURNING inventory_item_uuid`,
		"AREAU-"+suffix, "Area Item "+suffix, unit).Scan(&uuid); err != nil {
		t.Fatalf("seed area item in %s: %v", unit, err)
	}
	return uuid
}

// A line that sends no unit takes its item's: 120 x 60 inches is 50 sq ft, so two
// slabs are 100 sq ft = $1000.00 at $10 / sq ft.
func TestCreate_TakesTheItemsDimensionUnitWhenNoneIsSent(t *testing.T) {
	pool := testPool(t)
	custUUID, _ := seedCustomerAndItem(t, pool)
	itemUUID := seedAreaItemIn(t, pool, "in")

	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Length: f64(120), Width: f64(60), Slabs: ip(2)}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	l := got.Items[0]
	if l.DimensionUnit != "in" {
		t.Errorf("DimensionUnit = %q, want in (the item's)", l.DimensionUnit)
	}
	if l.Quantity != 100 || l.LineSubtotal != 1000 {
		t.Errorf("qty = %v, subtotal = %v, want 100 and 1000", l.Quantity, l.LineSubtotal)
	}
}

// The unit the screen showed is the unit that is saved, even when the item has
// since been switched to another.
func TestCreate_TheLinesOwnUnitWinsOverTheItems(t *testing.T) {
	pool := testPool(t)
	custUUID, _ := seedCustomerAndItem(t, pool)
	itemUUID := seedAreaItemIn(t, pool, "cm")

	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Length: f64(10), Width: f64(5), DimensionUnit: "ft"}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	l := got.Items[0]
	if l.DimensionUnit != "ft" || l.Quantity != 50 {
		t.Errorf("unit = %q, qty = %v, want ft and 50", l.DimensionUnit, l.Quantity)
	}
}

func TestCreate_RefusesAnUnknownLineUnit(t *testing.T) {
	pool := testPool(t)
	custUUID, _ := seedCustomerAndItem(t, pool)
	itemUUID := seedAreaItem(t, pool)

	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Length: f64(10), Width: f64(5), DimensionUnit: "yd"}}
	_, err := Create(context.Background(), pool, in, 1)
	if err == nil || !IsClientError(err) || !strings.Contains(err.Error(), "Line 1: the dimension unit must be one of") {
		t.Fatalf("err = %v, want the unit refused", err)
	}
}

// Estimate → Quote keeps the unit with the numbers, so 120 never turns into 120 cm.
func TestConvertFromEstimate_CarriesTheDimensionUnit(t *testing.T) {
	pool := testPool(t)
	custUUID, _ := seedCustomerAndItem(t, pool)
	itemUUID := seedAreaItemIn(t, pool, "in")

	in := estimate.CreateEstimateInput{CustomerUUID: custUUID}
	in.Items = []estimate.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Length: f64(120), Width: f64(60), Slabs: ip(1)}}
	est, err := estimate.Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("seed estimate: %v", err)
	}

	got, _, err := ConvertFromEstimate(context.Background(), pool, est.ID, 1)
	if err != nil {
		t.Fatalf("ConvertFromEstimate: %v", err)
	}
	l := got.Items[0]
	if l.DimensionUnit != "in" || l.Quantity != 50 || l.Length == nil || *l.Length != 120 {
		t.Errorf("quote line = unit %q qty %v length %v, want in / 50 / 120", l.DimensionUnit, l.Quantity, l.Length)
	}
}
