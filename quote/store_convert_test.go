// quote/store_convert_test.go
//go:build dbtest

package quote

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/estimate"
)

// seedEstimateWithLine creates a live estimate with one catalog-priced line,
// for convert tests. estimateFields is unexported in the estimate package,
// but its promoted Items/SalesTaxPercent fields are still settable on the
// embedding CreateEstimateInput from outside the package.
func seedEstimateWithLine(t *testing.T, pool *pgxpool.Pool, custUUID, itemUUID string) *estimate.Estimate {
	t.Helper()
	in := estimate.CreateEstimateInput{CustomerUUID: custUUID}
	in.SalesTaxPercent = 8
	in.Items = []estimate.LineInput{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2},
	}
	est, err := estimate.Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("seed estimate: %v", err)
	}
	return est
}

func TestConvertFromEstimate_CopiesLinesAndTotals(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	est := seedEstimateWithLine(t, pool, custUUID, itemUUID)

	got, created, err := ConvertFromEstimate(context.Background(), pool, est.ID, 1)
	if err != nil {
		t.Fatalf("ConvertFromEstimate: %v", err)
	}
	if !created {
		t.Errorf("created = false on first conversion, want true")
	}
	if got.EstimateUUID == nil || *got.EstimateUUID != est.ID {
		t.Errorf("EstimateUUID = %v, want %v", got.EstimateUUID, est.ID)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
	// unit price snapshotted from inventory_item (25.00) since estimate left UnitPrice at 0.
	if got.Items[0].UnitPrice != 25 {
		t.Errorf("Items[0].UnitPrice = %v, want 25", got.Items[0].UnitPrice)
	}
	// subtotal = 2*25=50, tax = 50*0.08=4, grand = 54 — must match the estimate's totals exactly
	// since lines are copied verbatim, not re-priced.
	if got.GrandTotal != est.GrandTotal {
		t.Errorf("GrandTotal = %v, want %v (estimate's total)", got.GrandTotal, est.GrandTotal)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}
}

func TestConvertFromEstimate_IsIdempotent(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	est := seedEstimateWithLine(t, pool, custUUID, itemUUID)

	first, created1, err := ConvertFromEstimate(context.Background(), pool, est.ID, 1)
	if err != nil {
		t.Fatalf("first ConvertFromEstimate: %v", err)
	}
	if !created1 {
		t.Fatalf("created1 = false, want true")
	}

	second, created2, err := ConvertFromEstimate(context.Background(), pool, est.ID, 1)
	if err != nil {
		t.Fatalf("second ConvertFromEstimate: %v", err)
	}
	if created2 {
		t.Errorf("created2 = true, want false (replay should not create a duplicate)")
	}
	if second.ID != first.ID {
		t.Errorf("second.ID = %q, want %q (same quote)", second.ID, first.ID)
	}
}

func TestConvertFromEstimate_UnknownEstimate(t *testing.T) {
	pool := testPool(t)
	_, _, err := ConvertFromEstimate(context.Background(), pool, "00000000-0000-0000-0000-000000000000", 1)
	if !errors.Is(err, ErrEstimateNotFound) {
		t.Fatalf("ConvertFromEstimate with unknown uuid: err = %v, want ErrEstimateNotFound", err)
	}
}
