//go:build dbtest

package salesorder

import (
	"context"
	"strings"
	"testing"

	"stonesuite-backend/quote"
)

// A kit is only a label on a run of ordinary lines, so any well-formed uuid will
// do — the document never looks the kit up.
const (
	kitUUID      = "7d0c1f2e-4a3b-4c5d-8e6f-0123456789ab"
	otherKitUUID = "11111111-2222-4333-8444-555555555555"
)

func TestCreate_StoresTheKitALineWasAddedFrom(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateOrderInput{CustomerUUID: custUUID}
	in.Items = []LineInput2{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: strings.ToUpper(kitUUID), KitName: "  Kitchen "},
		{LineNumber: 2, InventoryItemUUID: itemUUID, Quantity: 2, KitUUID: kitUUID, KitName: "Kitchen"},
		{LineNumber: 3, InventoryItemUUID: itemUUID, Quantity: 1},
	}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(got.Items) != 3 {
		t.Fatalf("len(Items) = %d, want 3", len(got.Items))
	}
	for i := 0; i < 2; i++ {
		l := got.Items[i]
		if l.KitID == nil || *l.KitID != kitUUID || l.KitName != "Kitchen" {
			t.Errorf("line %d kit = (%v, %q), want (%s, Kitchen)", i+1, l.KitID, l.KitName, kitUUID)
		}
	}
	if l := got.Items[2]; l.KitID != nil || l.KitName != "" {
		t.Errorf("line 3 kit = (%v, %q), want none", l.KitID, l.KitName)
	}
}

// The kit label changes nothing about a line's money.
func TestCreate_KitDoesNotChangeTheTotals(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	build := func(kit bool) *Order {
		in := CreateOrderInput{CustomerUUID: custUUID}
		in.SalesTaxPercent = 8
		line := LineInput2{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2}
		if kit {
			line.KitUUID, line.KitName = kitUUID, "Kitchen"
		}
		in.Items = []LineInput2{line}
		o, err := Create(context.Background(), pool, in, 1)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		return o
	}
	plain, kitted := build(false), build(true)
	if plain.GrandTotal != kitted.GrandTotal || plain.Items[0].LineTotal != kitted.Items[0].LineTotal {
		t.Errorf("totals differ: plain %v / %v, kitted %v / %v",
			plain.GrandTotal, plain.Items[0].LineTotal, kitted.GrandTotal, kitted.Items[0].LineTotal)
	}
}

func TestCreate_RefusesAMalformedKitLabel(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	cases := []struct {
		name, uuid, kitName, want string
	}{
		{"not a uuid", "kitchen", "Kitchen", "Line 1: the kit id is not valid."},
		{"no name", kitUUID, "", "Line 1: a kit line needs the kit's name."},
		{"name too long", kitUUID, strings.Repeat("k", 101), "Line 1: the kit name is too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := CreateOrderInput{CustomerUUID: custUUID}
			in.Items = []LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: tc.uuid, KitName: tc.kitName}}
			_, err := Create(context.Background(), pool, in, 1)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestUpdate_ReplacesTheKitLabelsWithTheSavedLines(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateOrderInput{CustomerUUID: custUUID}
	in.Items = []LineInput2{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: kitUUID, KitName: "Kitchen"},
		{LineNumber: 2, InventoryItemUUID: itemUUID, Quantity: 1},
	}
	order, err := Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	upd := UpdateOrderInput{}
	upd.Items = []LineInput2{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1},
		{LineNumber: 2, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: otherKitUUID, KitName: "Bathroom"},
	}
	got, err := Update(ctx, pool, order.ID, upd, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if l := got.Items[0]; l.KitID != nil || l.KitName != "" {
		t.Errorf("line 1 should have left its kit, got (%v, %q)", l.KitID, l.KitName)
	}
	if l := got.Items[1]; l.KitID == nil || *l.KitID != otherKitUUID || l.KitName != "Bathroom" {
		t.Errorf("line 2 should be under Bathroom, got (%v, %q)", l.KitID, l.KitName)
	}
}

// Quote → Sales Order copies each line verbatim, kit heading included.
func TestConvertFromQuote_CarriesTheKitHeading(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := quote.CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []quote.LineInput{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: kitUUID, KitName: "Kitchen"},
		{LineNumber: 2, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: kitUUID, KitName: "Kitchen"},
		{LineNumber: 3, InventoryItemUUID: itemUUID, Quantity: 1},
	}
	q, err := quote.Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("seed quote: %v", err)
	}

	got, _, err := ConvertFromQuote(context.Background(), pool, q.ID, 1)
	if err != nil {
		t.Fatalf("ConvertFromQuote: %v", err)
	}
	if len(got.Items) != 3 {
		t.Fatalf("len(Items) = %d, want 3", len(got.Items))
	}
	for i := 0; i < 2; i++ {
		if l := got.Items[i]; l.KitID == nil || *l.KitID != kitUUID || l.KitName != "Kitchen" {
			t.Errorf("order line %d lost its kit: (%v, %q)", i+1, l.KitID, l.KitName)
		}
	}
	if l := got.Items[2]; l.KitID != nil {
		t.Errorf("order line 3 gained a kit: %v", *l.KitID)
	}
}
