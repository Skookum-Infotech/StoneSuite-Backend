//go:build dbtest

package inventory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func itemInputIn(unit string) CreateItemInput {
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	return CreateItemInput{
		SKU: "DIM-" + suffix, Name: "Dimension Item " + suffix, UnitID: 1,
		UnitPrice: priceOf(10), PurchaseUnitPrice: priceOf(6), DimensionUnit: unit,
	}
}

// An item with no stated unit is measured in inches.
func TestCreate_DimensionUnitDefaultsToInches(t *testing.T) {
	pool := testPool(t)
	got, err := Create(context.Background(), pool, itemInputIn(""), 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.DimensionUnit != "in" {
		t.Errorf("DimensionUnit = %q, want in", got.DimensionUnit)
	}
}

func TestCreate_StoresTheStatedDimensionUnit(t *testing.T) {
	pool := testPool(t)
	got, err := Create(context.Background(), pool, itemInputIn(" IN "), 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.DimensionUnit != "in" {
		t.Errorf("DimensionUnit = %q, want in", got.DimensionUnit)
	}
}

func TestCreate_RefusesAnUnknownDimensionUnit(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, itemInputIn("yd"), 1)
	if err == nil || !IsClientError(err) || !strings.Contains(err.Error(), "must be one of mm, cm, in, ft, m") {
		t.Fatalf("err = %v, want the unit refused", err)
	}
}

// Update is a whole-object write; a client that predates the field omits it, and
// must not silently put a feet item back to the default by saving.
func TestUpdate_OmittedDimensionUnitLeavesItAlone(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	created, err := Create(ctx, pool, itemInputIn("ft"), 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	in := itemInputIn("")
	in.SKU, in.Name = created.SKU, created.Name
	if err := Update(ctx, pool, created.ID, in, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := Get(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DimensionUnit != "ft" {
		t.Errorf("DimensionUnit = %q, want it kept as ft", got.DimensionUnit)
	}
}

func TestUpdate_ChangesTheDimensionUnit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	created, err := Create(ctx, pool, itemInputIn("cm"), 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	in := itemInputIn("ft")
	in.SKU, in.Name = created.SKU, created.Name
	if err := Update(ctx, pool, created.ID, in, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := Get(ctx, pool, created.ID)
	if got.DimensionUnit != "ft" {
		t.Errorf("DimensionUnit = %q, want ft", got.DimensionUnit)
	}
}
