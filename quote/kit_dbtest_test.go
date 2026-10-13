//go:build dbtest

package quote

import (
	"context"
	"strings"
	"testing"

	"stonesuite-backend/estimate"
)

// The kit label is not looked up, so any well-formed uuid will do.
const kitUUID = "7d0c1f2e-4a3b-4c5d-8e6f-0123456789ab"

func TestCreate_StoresTheKitALineWasAddedFrom(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateQuoteInput{CustomerUUID: custUUID}
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

	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: "kitchen", KitName: "Kitchen"}}
	_, err := Create(context.Background(), pool, in, 1)
	if err == nil || !strings.Contains(err.Error(), "Line 1: the kit id is not valid.") {
		t.Fatalf("err = %v, want the kit id refused", err)
	}
}

func TestUpdate_ReplacesTheKitLabelsWithTheSavedLines(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: kitUUID, KitName: "Kitchen"}}
	q, err := Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	upd := UpdateQuoteInput{}
	upd.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	got, err := Update(ctx, pool, q.ID, upd, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if l := got.Items[0]; l.KitID != nil || l.KitName != "" {
		t.Errorf("the saved line should have left its kit, got (%v, %q)", l.KitID, l.KitName)
	}
}

// Estimate → Quote copies each line verbatim, kit heading included.
func TestConvertFromEstimate_CarriesTheKitHeading(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := estimate.CreateEstimateInput{CustomerUUID: custUUID}
	in.Items = []estimate.LineInput{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: kitUUID, KitName: "Kitchen"},
		{LineNumber: 2, InventoryItemUUID: itemUUID, Quantity: 1},
	}
	est, err := estimate.Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("seed estimate: %v", err)
	}
	if l := est.Items[0]; l.KitID == nil || l.KitName != "Kitchen" {
		t.Fatalf("estimate line lost its kit: (%v, %q)", l.KitID, l.KitName)
	}

	got, _, err := ConvertFromEstimate(context.Background(), pool, est.ID, 1)
	if err != nil {
		t.Fatalf("ConvertFromEstimate: %v", err)
	}
	if l := got.Items[0]; l.KitID == nil || *l.KitID != kitUUID || l.KitName != "Kitchen" {
		t.Errorf("quote line 1 lost its kit: (%v, %q)", l.KitID, l.KitName)
	}
	if l := got.Items[1]; l.KitID != nil {
		t.Errorf("quote line 2 gained a kit: %v", *l.KitID)
	}
}
