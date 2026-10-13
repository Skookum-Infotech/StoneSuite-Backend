//go:build dbtest

package estimate

import (
	"context"
	"strings"
	"testing"
)

// The kit label is not looked up, so any well-formed uuid will do.
const kitUUID = "7d0c1f2e-4a3b-4c5d-8e6f-0123456789ab"

func TestCreate_StoresTheKitALineWasAddedFrom(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateEstimateInput{CustomerUUID: custUUID}
	in.Items = []LineInput{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: kitUUID, KitName: " Kitchen "},
		{LineNumber: 2, InventoryItemUUID: itemUUID, Quantity: 1},
	}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if l := got.Items[0]; l.KitID == nil || *l.KitID != kitUUID || l.KitName != "Kitchen" {
		t.Errorf("line 1 kit = (%v, %q), want (%s, Kitchen)", l.KitID, l.KitName, kitUUID)
	}
	if l := got.Items[1]; l.KitID != nil || l.KitName != "" {
		t.Errorf("line 2 kit = (%v, %q), want none", l.KitID, l.KitName)
	}
}

func TestCreate_RefusesAMalformedKitLabel(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateEstimateInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: kitUUID, KitName: ""}}
	_, err := Create(context.Background(), pool, in, 1)
	if err == nil || !strings.Contains(err.Error(), "Line 1: a kit line needs the kit's name.") {
		t.Fatalf("err = %v, want the missing kit name refused", err)
	}
}

func TestUpdate_ReplacesTheKitLabelsWithTheSavedLines(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateEstimateInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	est, err := Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	upd := UpdateEstimateInput{}
	upd.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: kitUUID, KitName: "Kitchen"}}
	got, err := Update(ctx, pool, est.ID, upd, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if l := got.Items[0]; l.KitID == nil || *l.KitID != kitUUID || l.KitName != "Kitchen" {
		t.Errorf("the saved line should be under Kitchen, got (%v, %q)", l.KitID, l.KitName)
	}
}
