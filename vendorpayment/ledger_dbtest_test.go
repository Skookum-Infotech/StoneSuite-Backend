//go:build dbtest

package vendorpayment

import (
	"context"
	"math"
	"testing"

	"stonesuite-backend/dbtestutil"
	"stonesuite-backend/glpost"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

// A vendor payment reaches the ledger only once it is Paid (the money has gone
// out), and voiding a Paid payment returns it.
func TestLedger_PaidPostsAndVoidReverses(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bank := dbtestutil.SlotBalance(t, pool, glpost.SlotBank)

	vp := newPayment(t, pool, seedVendor(t, pool), 60)
	if live, _ := dbtestutil.LedgerEntries(t, pool, glpost.SourceVendorPayment, vp.ID); live != 0 {
		t.Fatalf("a draft payment has %d entries; want none", live)
	}
	for _, st := range []string{"PAPV", "PAID"} { // no approver: PAPV auto-skips to APPV
		var err error
		if vp, err = Transition(ctx, pool, vp.ID, st, 1); err != nil {
			t.Fatalf("transition to %s: %v", st, err)
		}
	}
	lines := dbtestutil.EntryLines(t, pool, glpost.SourceVendorPayment, vp.ID)
	if !near(lines[glpost.SlotAP], 60) || !near(lines[glpost.SlotBank], -60) {
		t.Errorf("entry = %v; want Dr AP 60 / Cr Bank 60", lines)
	}
	if got := dbtestutil.SlotBalance(t, pool, glpost.SlotBank) - bank; !near(got, -60) {
		t.Errorf("Bank moved by %v, want -60", got)
	}

	if _, err := Transition(ctx, pool, vp.ID, "VOID", 1); err != nil {
		t.Fatalf("void: %v", err)
	}
	if live, rev := dbtestutil.LedgerEntries(t, pool, glpost.SourceVendorPayment, vp.ID); live != 1 || rev != 1 {
		t.Errorf("after void: %d live, %d reversal; want 1, 1", live, rev)
	}
	if got := dbtestutil.SlotBalance(t, pool, glpost.SlotBank) - bank; !near(got, 0) {
		t.Errorf("Bank after void moved by %v, want 0", got)
	}
}
