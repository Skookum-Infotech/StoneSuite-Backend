// salesorder/store_convert_test.go
//go:build dbtest

package salesorder

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/quote"
)

// seedQuoteWithLine creates a live quote with one catalog-priced line, for
// convert tests. quoteFields is unexported in the quote package, but its
// promoted Items/SalesTaxPercent fields are still settable on the embedding
// CreateQuoteInput from outside the package.
func seedQuoteWithLine(t *testing.T, pool *pgxpool.Pool, custUUID, itemUUID string) *quote.Quote {
	t.Helper()
	in := quote.CreateQuoteInput{CustomerUUID: custUUID}
	in.SalesTaxPercent = 8
	in.Items = []quote.LineInput{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2},
	}
	q, err := quote.Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("seed quote: %v", err)
	}
	return q
}

func TestConvertFromQuote_CopiesLinesAndTotals(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	q := seedQuoteWithLine(t, pool, custUUID, itemUUID)

	got, created, err := ConvertFromQuote(context.Background(), pool, q.ID, 1)
	if err != nil {
		t.Fatalf("ConvertFromQuote: %v", err)
	}
	if !created {
		t.Errorf("created = false on first conversion, want true")
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
	if got.Items[0].UnitPrice != 25 {
		t.Errorf("Items[0].UnitPrice = %v, want 25", got.Items[0].UnitPrice)
	}
	if got.GrandTotal != q.GrandTotal {
		t.Errorf("GrandTotal = %v, want %v (quote's total)", got.GrandTotal, q.GrandTotal)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM quote_conversion WHERE quote_id = (SELECT quote_id FROM quote WHERE quote_uuid = $1)`,
		q.ID).Scan(&count); err != nil {
		t.Fatalf("count quote_conversion rows: %v", err)
	}
	if count != 1 {
		t.Errorf("quote_conversion rows = %d, want 1", count)
	}
}

func TestConvertFromQuote_IsIdempotent(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	q := seedQuoteWithLine(t, pool, custUUID, itemUUID)

	first, created1, err := ConvertFromQuote(context.Background(), pool, q.ID, 1)
	if err != nil {
		t.Fatalf("first ConvertFromQuote: %v", err)
	}
	if !created1 {
		t.Fatalf("created1 = false, want true")
	}

	second, created2, err := ConvertFromQuote(context.Background(), pool, q.ID, 1)
	if err != nil {
		t.Fatalf("second ConvertFromQuote: %v", err)
	}
	if created2 {
		t.Errorf("created2 = true, want false (replay should not create a duplicate)")
	}
	if second.ID != first.ID {
		t.Errorf("second.ID = %q, want %q (same order)", second.ID, first.ID)
	}
}

func TestConvertFromQuote_UnknownQuote(t *testing.T) {
	pool := testPool(t)
	_, _, err := ConvertFromQuote(context.Background(), pool, "00000000-0000-0000-0000-000000000000", 1)
	if !errors.Is(err, ErrQuoteNotFound) {
		t.Fatalf("ConvertFromQuote with unknown uuid: err = %v, want ErrQuoteNotFound", err)
	}
}

// A quote becomes an order under the same stock rules as one entered by hand.
func TestConvertFromQuote_RefusedWhenStockIsShort(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	q := seedQuoteWithLine(t, pool, custUUID, itemUUID) // asks for 2
	giveStock(t, pool, itemUUID, 1)

	_, created, err := ConvertFromQuote(context.Background(), pool, q.ID, 1)

	short := shortageOf(t, err)
	if created || short[0].Requested != 2 || short[0].Available != 1 || short[0].Short != 1 {
		t.Errorf("created=%v shortage=%+v, want refused: requested 2 available 1 short 1", created, short[0])
	}
	// Refused means no order and no conversion record: the quote can convert later.
	var conversions int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM quote_conversion WHERE quote_id = (SELECT quote_id FROM quote WHERE quote_uuid = $1)`,
		q.ID).Scan(&conversions); err != nil {
		t.Fatal(err)
	}
	if conversions != 0 {
		t.Errorf("quote_conversion rows = %d after a refused conversion, want 0", conversions)
	}
	giveStock(t, pool, itemUUID, 10)
	if _, created, err := ConvertFromQuote(context.Background(), pool, q.ID, 1); err != nil || !created {
		t.Fatalf("converting after restocking: created=%v err=%v", created, err)
	}
}

func TestConvertFromQuote_ReservesTheStockItNeeds(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	q := seedQuoteWithLine(t, pool, custUUID, itemUUID) // asks for 2
	giveStock(t, pool, itemUUID, 3)

	order, _, err := ConvertFromQuote(context.Background(), pool, q.ID, 1)
	if err != nil {
		t.Fatalf("ConvertFromQuote: %v", err)
	}

	if got := held(t, pool, order.ID, itemUUID); got != 2 {
		t.Errorf("converted order holds %v, want 2", got)
	}
	// One is left, so a second order for 2 is refused.
	_, err = Create(context.Background(), pool, orderFor(seedCustomer(t, pool), lines(line{itemUUID, 2})), 1)
	if got := shortageOf(t, err)[0].Available; got != 1 {
		t.Errorf("available = %v after the conversion, want 1", got)
	}
}
