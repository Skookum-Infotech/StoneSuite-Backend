//go:build dbtest

package glpost

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testTx(t *testing.T) (context.Context, pgx.Tx) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping dbtest")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) }) // nothing here is ever committed
	return ctx, tx
}

func newSourceID(t *testing.T, ctx context.Context, tx pgx.Tx) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("new uuid: %v", err)
	}
	return id
}

// balanceOf is the account's running balance (debit positive) by default-slot key.
func balanceOf(t *testing.T, ctx context.Context, tx pgx.Tx, slot string) float64 {
	t.Helper()
	var b float64
	if err := tx.QueryRow(ctx, `
		SELECT a.coa_account_balance FROM coa_default_mapping m
		JOIN coa_account a ON a.coa_account_id = m.coa_account_id WHERE m.slot_key = $1`, slot).Scan(&b); err != nil {
		t.Fatalf("balance of %s: %v", slot, err)
	}
	return b
}

func entryCount(t *testing.T, ctx context.Context, tx pgx.Tx, source, id string) (live, reversals int) {
	t.Helper()
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE NOT is_reversal), COUNT(*) FILTER (WHERE is_reversal)
		FROM journal_entry WHERE source_type = $1 AND source_id = $2::uuid`, source, id).Scan(&live, &reversals); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	return live, reversals
}

func invoicePosting(id string) Posting {
	return Posting{
		SourceType: SourceInvoice, SourceID: id, Date: time.Now().UTC(), Memo: "Invoice test",
		// 100.00 subtotal - 10.00 discount + 9.00 tax = 99.00 grand.
		Legs: InvoiceLegs(InvoiceAmounts{Discount: 1000, Tax: 900, Grand: 9900}),
	}
}

func TestPostOnce_PostsBalancedEntryAndMovesAccounts(t *testing.T) {
	ctx, tx := testTx(t)
	id := newSourceID(t, ctx, tx)
	ar, rev, disc, tax := balanceOf(t, ctx, tx, SlotAR), balanceOf(t, ctx, tx, SlotSalesRevenue),
		balanceOf(t, ctx, tx, SlotSalesDiscount), balanceOf(t, ctx, tx, SlotTaxPayable)

	posted, err := PostOnce(ctx, tx, invoicePosting(id))
	if err != nil || !posted {
		t.Fatalf("PostOnce = %v, %v; want true, nil", posted, err)
	}
	// Debit-positive balances: AR +99, discount +10, revenue -100 (credit), tax payable -9.
	for _, c := range []struct {
		slot      string
		before    float64
		wantDelta float64
	}{{SlotAR, ar, 99}, {SlotSalesDiscount, disc, 10}, {SlotSalesRevenue, rev, -100}, {SlotTaxPayable, tax, -9}} {
		if got := balanceOf(t, ctx, tx, c.slot) - c.before; got != c.wantDelta {
			t.Errorf("%s moved by %v, want %v", c.slot, got, c.wantDelta)
		}
	}
	if live, rev := entryCount(t, ctx, tx, SourceInvoice, id); live != 1 || rev != 0 {
		t.Errorf("entries = %d live, %d reversal; want 1, 0", live, rev)
	}
}

func TestPostOnce_IsIdempotent(t *testing.T) {
	ctx, tx := testTx(t)
	id := newSourceID(t, ctx, tx)
	if posted, err := PostOnce(ctx, tx, invoicePosting(id)); err != nil || !posted {
		t.Fatalf("first PostOnce = %v, %v", posted, err)
	}
	before := balanceOf(t, ctx, tx, SlotAR)
	if posted, err := PostOnce(ctx, tx, invoicePosting(id)); err != nil || posted {
		t.Fatalf("second PostOnce = %v, %v; want false, nil", posted, err)
	}
	if got := balanceOf(t, ctx, tx, SlotAR); got != before {
		t.Errorf("a repeat post moved AR from %v to %v", before, got)
	}
	if live, _ := entryCount(t, ctx, tx, SourceInvoice, id); live != 1 {
		t.Errorf("live entries = %d, want 1", live)
	}
}

func TestPostOnce_NothingToPostForZeroValueDocument(t *testing.T) {
	ctx, tx := testTx(t)
	id := newSourceID(t, ctx, tx)
	p := invoicePosting(id)
	p.Legs = InvoiceLegs(InvoiceAmounts{})
	if posted, err := PostOnce(ctx, tx, p); err != nil || posted {
		t.Fatalf("PostOnce = %v, %v; want false, nil", posted, err)
	}
	if live, _ := entryCount(t, ctx, tx, SourceInvoice, id); live != 0 {
		t.Errorf("a zero-value document must not write an entry, got %d", live)
	}
}

func TestReverseIfPosted_NetsToZeroOnceAndIgnoresUnposted(t *testing.T) {
	ctx, tx := testTx(t)
	id := newSourceID(t, ctx, tx)

	if reversed, err := ReverseIfPosted(ctx, tx, SourceInvoice, id, Today(), "void", 0); err != nil || reversed {
		t.Fatalf("reversing a document with no entry = %v, %v; want false, nil", reversed, err)
	}

	start := balanceOf(t, ctx, tx, SlotAR)
	if _, err := PostOnce(ctx, tx, invoicePosting(id)); err != nil {
		t.Fatal(err)
	}
	if reversed, err := ReverseIfPosted(ctx, tx, SourceInvoice, id, Today(), "void", 0); err != nil || !reversed {
		t.Fatalf("ReverseIfPosted = %v, %v; want true, nil", reversed, err)
	}
	if got := balanceOf(t, ctx, tx, SlotAR); got != start {
		t.Errorf("AR after post+void = %v, want it back at %v", got, start)
	}
	if reversed, err := ReverseIfPosted(ctx, tx, SourceInvoice, id, Today(), "void", 0); err != nil || reversed {
		t.Fatalf("second ReverseIfPosted = %v, %v; want false, nil (already reversed)", reversed, err)
	}
	if live, rev := entryCount(t, ctx, tx, SourceInvoice, id); live != 1 || rev != 1 {
		t.Errorf("entries = %d live, %d reversal; want 1, 1", live, rev)
	}
}

func TestPostOnce_UnmappedAccountIsRefusedNamingIt(t *testing.T) {
	ctx, tx := testTx(t)
	if _, err := tx.Exec(ctx, `UPDATE coa_default_mapping SET coa_account_id = NULL WHERE slot_key = $1`, SlotAR); err != nil {
		t.Fatalf("unmap slot: %v", err)
	}
	id := newSourceID(t, ctx, tx)
	_, err := PostOnce(ctx, tx, invoicePosting(id))
	ue, ok := AsUserError(err)
	if !ok {
		t.Fatalf("want a UserError, got %v", err)
	}
	if want := "Accounts Receivable"; !strings.Contains(ue.Msg, want) {
		t.Errorf("message %q does not name %q", ue.Msg, want)
	}
	if live, _ := entryCount(t, ctx, tx, SourceInvoice, id); live != 0 {
		t.Errorf("a refused posting must write nothing, got %d entries", live)
	}
}

func TestPostOnce_ClosedPeriodIsRefused(t *testing.T) {
	ctx, tx := testTx(t)
	if _, err := tx.Exec(ctx,
		`UPDATE accounting_settings SET books_closed_through = $1 WHERE accounting_settings_id = 1`,
		time.Now().UTC().AddDate(0, 0, 30)); err != nil {
		t.Fatalf("close books: %v", err)
	}
	id := newSourceID(t, ctx, tx)
	_, err := PostOnce(ctx, tx, invoicePosting(id))
	ue, ok := AsUserError(err)
	if !ok || !strings.Contains(ue.Msg, "closed") {
		t.Fatalf("want a closed-period UserError, got %v", err)
	}
}
