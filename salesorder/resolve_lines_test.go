package salesorder

import (
	"context"
	"testing"
)

// TestResolveLinesFreeText covers a free-text line's SKU/ItemName/UnitCode/
// TaxPercent passing through as typed, since (unlike a catalog-linked line)
// there is no inventory_item row to snapshot them from.
func TestResolveLinesFreeText(t *testing.T) {
	ctx := context.Background()

	t.Run("sku/itemName/unitCode/taxPercent pass through untouched", func(t *testing.T) {
		items := []LineInput2{{
			LineNumber:  1,
			Description: "Custom fabricated bracket",
			SKU:         "  CUST-001  ",
			ItemName:    "  Custom Bracket  ",
			UnitCode:    " ea ",
			Quantity:    2,
			UnitPrice:   50,
			TaxPercent:  float64Ptr(7.5),
		}}
		got, err := resolveLines(ctx, fakeQuerier{}, items, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d lines, want 1", len(got))
		}
		l := got[0]
		if l.sku != "CUST-001" {
			t.Errorf("sku = %q, want %q", l.sku, "CUST-001")
		}
		if l.name != "Custom Bracket" {
			t.Errorf("name = %q, want %q", l.name, "Custom Bracket")
		}
		if l.unitCode != "ea" {
			t.Errorf("unitCode = %q, want %q", l.unitCode, "ea")
		}
		if l.taxPercent != 7.5 {
			t.Errorf("taxPercent = %v, want 7.5", l.taxPercent)
		}
	})

	t.Run("no taxPercent falls back to header tax", func(t *testing.T) {
		items := []LineInput2{{LineNumber: 1, Description: "desc", Quantity: 1, UnitPrice: 10}}
		got, err := resolveLines(ctx, fakeQuerier{}, items, 8.25)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got[0].taxPercent != 8.25 {
			t.Errorf("taxPercent = %v, want 8.25 (header default)", got[0].taxPercent)
		}
	})

	t.Run("out-of-range taxPercent is rejected", func(t *testing.T) {
		items := []LineInput2{{LineNumber: 1, Description: "desc", Quantity: 1, UnitPrice: 10, TaxPercent: float64Ptr(150)}}
		if _, err := resolveLines(ctx, fakeQuerier{}, items, 0); !IsClientError(err) {
			t.Errorf("expected ClientError, got %v", err)
		}
	})

	t.Run("negative taxPercent is rejected", func(t *testing.T) {
		items := []LineInput2{{LineNumber: 1, Description: "desc", Quantity: 1, UnitPrice: 10, TaxPercent: float64Ptr(-1)}}
		if _, err := resolveLines(ctx, fakeQuerier{}, items, 0); !IsClientError(err) {
			t.Errorf("expected ClientError, got %v", err)
		}
	})
}

func float64Ptr(f float64) *float64 { return &f }
