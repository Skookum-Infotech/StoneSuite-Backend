//go:build dbtest

package inventory

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCreate_StoresAndReturnsBothPrices(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created := newItem(t, pool, 1) // selling 25, purchasing 15
	if created.UnitPrice != 25 || created.PurchaseUnitPrice != 15 {
		t.Fatalf("created prices = %v selling / %v purchasing, want 25 / 15", created.UnitPrice, created.PurchaseUnitPrice)
	}

	got, err := Get(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UnitPrice != 25 || got.PurchaseUnitPrice != 15 {
		t.Errorf("read-back prices = %v selling / %v purchasing, want 25 / 15", got.UnitPrice, got.PurchaseUnitPrice)
	}
}

func TestCreate_RefusesAMissingOrNegativePrice(t *testing.T) {
	pool := testPool(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	tests := []struct {
		name              string
		unit, purchase    *float64
		wantFieldInDetail string
	}{
		{"selling omitted", nil, priceOf(5), "Selling unit price is required."},
		{"purchasing omitted", priceOf(5), nil, "Purchasing unit price is required."},
		{"purchasing negative", priceOf(5), priceOf(-1), "Purchasing unit price cannot be negative."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Create(context.Background(), pool, CreateItemInput{
				SKU: "PRICE-" + tc.name + suffix, Name: "Price Item " + tc.name + suffix, UnitID: 1,
				UnitPrice: tc.unit, PurchaseUnitPrice: tc.purchase,
			}, 1)
			if !IsClientError(err) {
				t.Fatalf("err = %v, want a client error", err)
			}
			if err.Error() != tc.wantFieldInDetail {
				t.Errorf("message = %q, want %q", err.Error(), tc.wantFieldInDetail)
			}
		})
	}
}

func TestUpdate_ChangesEachPriceAndRecordsOnlyWhatChanged(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := newItem(t, pool, 1) // selling 25, purchasing 15

	// Only the purchasing price moves — and to 0, which is a real price.
	if err := Update(ctx, pool, item.ID, CreateItemInput{
		SKU: item.SKU, Name: item.Name, UnitID: 1,
		UnitPrice: priceOf(25), PurchaseUnitPrice: priceOf(0),
	}, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := Get(ctx, pool, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UnitPrice != 25 || got.PurchaseUnitPrice != 0 {
		t.Errorf("prices after update = %v / %v, want 25 / 0", got.UnitPrice, got.PurchaseUnitPrice)
	}

	hist, err := ItemHistory(ctx, pool, item.ID)
	if err != nil {
		t.Fatalf("ItemHistory: %v", err)
	}
	var purchase, selling *ItemHistoryEntry
	for i := range hist {
		switch hist[i].Field {
		case "purchaseUnitPrice":
			purchase = &hist[i]
		case "unitPrice":
			selling = &hist[i]
		}
	}
	if selling != nil {
		t.Errorf("an unchanged selling price must not be recorded, got %+v", *selling)
	}
	if purchase == nil {
		t.Fatalf("the purchasing price change is not in the history: %+v", hist)
	}
	if purchase.OldValue != "15.00" || purchase.NewValue != "0.00" {
		t.Errorf("history = %q -> %q, want 15.00 -> 0.00", purchase.OldValue, purchase.NewValue)
	}
}

func TestUpdate_RefusesAMissingPriceAndLeavesTheItemAlone(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := newItem(t, pool, 1)

	err := Update(ctx, pool, item.ID, CreateItemInput{
		SKU: item.SKU, Name: "Renamed " + item.Name, UnitID: 1,
		UnitPrice: priceOf(25), // purchasing omitted
	}, 1)
	if !IsClientError(err) {
		t.Fatalf("err = %v, want a client error", err)
	}
	after, _ := Get(ctx, pool, item.ID)
	if after.Name != item.Name || after.PurchaseUnitPrice != 15 {
		t.Errorf("a refused save must change nothing, got name %q purchasing %v", after.Name, after.PurchaseUnitPrice)
	}
}
