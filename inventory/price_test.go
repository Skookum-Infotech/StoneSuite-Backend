package inventory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// priceOf returns a pointer to v, for the two required price fields of
// CreateItemInput.
func priceOf(v float64) *float64 { return &v }

// validItemInput is the smallest input validateItemInput accepts.
func validItemInput() CreateItemInput {
	return CreateItemInput{
		SKU: "SKU-1", Name: "Item", UnitID: 1,
		UnitPrice: priceOf(10), PurchaseUnitPrice: priceOf(6),
	}
}

func TestValidateItemInput_Prices(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(in *CreateItemInput)
		wantMsg string // empty = valid
	}{
		{"both prices set", func(in *CreateItemInput) {}, ""},
		{"zero is a price (a service or labour item)", func(in *CreateItemInput) {
			in.UnitPrice, in.PurchaseUnitPrice = priceOf(0), priceOf(0)
		}, ""},
		{"selling price omitted", func(in *CreateItemInput) { in.UnitPrice = nil }, "Selling unit price is required."},
		{"purchasing price omitted", func(in *CreateItemInput) { in.PurchaseUnitPrice = nil }, "Purchasing unit price is required."},
		{"selling price negative", func(in *CreateItemInput) { in.UnitPrice = priceOf(-0.01) }, "Selling unit price cannot be negative."},
		{"purchasing price negative", func(in *CreateItemInput) { in.PurchaseUnitPrice = priceOf(-1) }, "Purchasing unit price cannot be negative."},
		{"both omitted reports the selling price first", func(in *CreateItemInput) {
			in.UnitPrice, in.PurchaseUnitPrice = nil, nil
		}, "Selling unit price is required."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validItemInput()
			tc.mutate(&in)

			err := validateItemInput(&in)

			if tc.wantMsg == "" {
				if err != nil {
					t.Fatalf("validateItemInput = %v, want nil", err)
				}
				return
			}
			var ce ClientError
			if !errors.As(err, &ce) {
				t.Fatalf("validateItemInput = %v, want a ClientError", err)
			}
			if ce.Msg != tc.wantMsg {
				t.Errorf("message = %q, want %q", ce.Msg, tc.wantMsg)
			}
		})
	}
}

func TestItemDiff_Prices(t *testing.T) {
	before := Item{SKU: "S", Name: "N", UnitPrice: 10, PurchaseUnitPrice: 6, Tracking: TrackingQuantity}
	in := validItemInput()
	in.SKU, in.Name, in.Tracking = "S", "N", TrackingQuantity

	if got := itemDiff(before, in); len(got) != 0 {
		t.Fatalf("an unchanged item reports a diff: %+v", got)
	}

	in.UnitPrice, in.PurchaseUnitPrice = priceOf(12.5), priceOf(7)
	got := itemDiff(before, in)

	want := map[string][2]string{
		"unitPrice":         {"10.00", "12.50"},
		"purchaseUnitPrice": {"6.00", "7.00"},
	}
	if len(got) != len(want) {
		t.Fatalf("diff = %+v, want exactly %v", got, want)
	}
	for _, d := range got {
		w, ok := want[d.field]
		if !ok || d.old != w[0] || d.new != w[1] {
			t.Errorf("diff %s: %q -> %q, want %v", d.field, d.old, d.new, w)
		}
	}
}

// The frontend reads `unitPrice` as the selling price and `purchaseUnitPrice`
// as what a purchase line defaults to; neither key may be renamed or dropped.
func TestItem_JSONCarriesBothPrices(t *testing.T) {
	b, err := json.Marshal(Item{UnitPrice: 12.5, PurchaseUnitPrice: 7})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["unitPrice"] != 12.5 {
		t.Errorf("unitPrice = %v, want 12.5 (selling)", got["unitPrice"])
	}
	if got["purchaseUnitPrice"] != 7.0 {
		t.Errorf("purchaseUnitPrice = %v, want 7", got["purchaseUnitPrice"])
	}
}

// An explicit 0 is a price; an absent field is not. The pointer fields are what
// let the server tell the two apart.
func TestCreateItemInput_JSONTellsAnOmittedPriceFromZero(t *testing.T) {
	var in CreateItemInput
	body := `{"sku":"s","name":"n","unitId":1,"unitPrice":0}`
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in.UnitPrice == nil || *in.UnitPrice != 0 {
		t.Errorf("UnitPrice = %v, want a pointer to 0", in.UnitPrice)
	}
	if in.PurchaseUnitPrice != nil {
		t.Errorf("PurchaseUnitPrice = %v, want nil for an omitted field", *in.PurchaseUnitPrice)
	}
	var ce ClientError
	if err := validateItemInput(&in); !errors.As(err, &ce) || ce.Msg != "Purchasing unit price is required." {
		t.Errorf("validate = %v, want the purchasing-price-required error", err)
	}
}

func TestItemDiff_PurchasePriceChangeAloneIsRecorded(t *testing.T) {
	before := Item{SKU: "S", Name: "N", UnitPrice: 10, PurchaseUnitPrice: 6, Tracking: TrackingQuantity}
	in := validItemInput()
	in.SKU, in.Name, in.Tracking = "S", "N", TrackingQuantity
	in.PurchaseUnitPrice = priceOf(6.5)

	got := itemDiff(before, in)

	if len(got) != 1 || got[0].field != "purchaseUnitPrice" {
		t.Fatalf("diff = %+v, want only purchaseUnitPrice", got)
	}
	if !strings.HasPrefix(got[0].new, "6.5") {
		t.Errorf("new value = %q, want 6.50", got[0].new)
	}
}
