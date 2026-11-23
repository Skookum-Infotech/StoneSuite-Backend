//go:build dbtest

package invoice_test

import (
	"context"
	"math"
	"testing"
	"time"

	"stonesuite-backend/dbtestutil"
	"stonesuite-backend/glpost"
	"stonesuite-backend/invoice"
	"stonesuite-backend/reports"
)

// zeroTime leaves the P&L window unbounded.
var zeroTime time.Time

func approxEq(a, b float64) bool { return math.Abs(a-b) < 0.005 }

func plIncome(t *testing.T) float64 {
	t.Helper()
	pool := testPool(t)
	pl, err := reports.ProfitAndLoss(context.Background(), pool, zeroTime, zeroTime)
	if err != nil {
		t.Fatalf("ProfitAndLoss: %v", err)
	}
	return pl.Income
}

// TestLedger_SentInvoicePostsAndShowsOnProfitAndLoss is the headline check:
// before this, an approved invoice never reached the ledger, so the P&L stayed
// empty no matter how much was billed.
func TestLedger_SentInvoicePostsAndShowsOnProfitAndLoss(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	incomeBefore := plIncome(t)
	arBefore := dbtestutil.SlotBalance(t, pool, glpost.SlotAR)

	_, invUUID := seedSentInvoice(t, pool, 250)

	if live, rev := dbtestutil.LedgerEntries(t, pool, glpost.SourceInvoice, invUUID); live != 1 || rev != 0 {
		t.Fatalf("entries = %d live, %d reversal; want 1, 0", live, rev)
	}
	lines := dbtestutil.EntryLines(t, pool, glpost.SourceInvoice, invUUID)
	if !approxEq(lines[glpost.SlotAR], 250) || !approxEq(lines[glpost.SlotSalesRevenue], -250) {
		t.Errorf("entry = %v; want Dr AR 250 / Cr Sales Revenue 250", lines)
	}
	if got := dbtestutil.SlotBalance(t, pool, glpost.SlotAR) - arBefore; !approxEq(got, 250) {
		t.Errorf("AR moved by %v, want 250", got)
	}
	if got := plIncome(t) - incomeBefore; !approxEq(got, 250) {
		t.Errorf("P&L income moved by %v, want 250", got)
	}

	// Moving on through Sent -> Overdue must not post a second time.
	if _, err := invoice.Transition(ctx, pool, invUUID, "ODUE", 1); err != nil {
		t.Fatalf("transition to ODUE: %v", err)
	}
	if live, _ := dbtestutil.LedgerEntries(t, pool, glpost.SourceInvoice, invUUID); live != 1 {
		t.Errorf("live entries after ODUE = %d, want still 1", live)
	}
}

func TestLedger_VoidReversesTheEntryAndTheProfitAndLossReturns(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	incomeBefore := plIncome(t)

	_, invUUID := seedSentInvoice(t, pool, 400)
	if got := plIncome(t) - incomeBefore; !approxEq(got, 400) {
		t.Fatalf("P&L income moved by %v after posting, want 400", got)
	}
	if _, err := invoice.Transition(ctx, pool, invUUID, "VOID", 1); err != nil {
		t.Fatalf("void: %v", err)
	}
	if live, rev := dbtestutil.LedgerEntries(t, pool, glpost.SourceInvoice, invUUID); live != 1 || rev != 1 {
		t.Errorf("entries = %d live, %d reversal; want 1, 1", live, rev)
	}
	if got := plIncome(t) - incomeBefore; !approxEq(got, 0) {
		t.Errorf("P&L income after void moved by %v, want it back at 0", got)
	}
}

func TestLedger_DraftInvoicePostsNothing(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t)
	inv, err := invoice.Create(context.Background(), pool, invoice.CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 90}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if live, rev := dbtestutil.LedgerEntries(t, pool, glpost.SourceInvoice, inv.ID); live != 0 || rev != 0 {
		t.Errorf("a draft has %d live / %d reversal entries; want none", live, rev)
	}
}
