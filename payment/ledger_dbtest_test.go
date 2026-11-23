//go:build dbtest

package payment

import (
	"context"
	"math"
	"testing"

	"stonesuite-backend/dbtestutil"
	"stonesuite-backend/glpost"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

func newLedgerPayment(t *testing.T, amount float64) *Payment {
	t.Helper()
	pool := testPool(t)
	p, err := Create(context.Background(), pool, CreatePaymentInput{
		CustomerUUID: seedCustomer(t, pool), MethodID: firstMethodID(t, pool), Amount: amount,
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return p
}

// With no approver configured a payment is born Approved, so its receipt goes
// on the books at creation; depositing it then banks the money.
func TestLedger_ReceiptThenDeposit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	undep := dbtestutil.SlotBalance(t, pool, glpost.SlotUndeposited)
	bank := dbtestutil.SlotBalance(t, pool, glpost.SlotBank)

	p := newLedgerPayment(t, 125)
	if p.StatusCode != "APPV" {
		t.Fatalf("status = %q, want APPV (no approver configured)", p.StatusCode)
	}
	lines := dbtestutil.EntryLines(t, pool, glpost.SourcePayment, p.ID)
	if !near(lines[glpost.SlotUndeposited], 125) || !near(lines[glpost.SlotAR], -125) {
		t.Errorf("receipt = %v; want Dr Undeposited 125 / Cr AR 125", lines)
	}
	if live, _ := dbtestutil.LedgerEntries(t, pool, glpost.SourcePaymentDeposit, p.ID); live != 0 {
		t.Fatalf("deposit entry exists before depositing")
	}

	if _, err := Transition(ctx, pool, p.ID, "DEPO", 1); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	dep := dbtestutil.EntryLines(t, pool, glpost.SourcePaymentDeposit, p.ID)
	if !near(dep[glpost.SlotBank], 125) || !near(dep[glpost.SlotUndeposited], -125) {
		t.Errorf("deposit = %v; want Dr Bank 125 / Cr Undeposited 125", dep)
	}
	if got := dbtestutil.SlotBalance(t, pool, glpost.SlotBank) - bank; !near(got, 125) {
		t.Errorf("Bank moved by %v, want 125", got)
	}
	if got := dbtestutil.SlotBalance(t, pool, glpost.SlotUndeposited) - undep; !near(got, 0) {
		t.Errorf("Undeposited net movement = %v, want 0 (in then out)", got)
	}
}

func TestLedger_VoidReversesTheReceipt(t *testing.T) {
	pool := testPool(t)
	ar := dbtestutil.SlotBalance(t, pool, glpost.SlotAR)

	p := newLedgerPayment(t, 80)
	if _, err := Transition(context.Background(), pool, p.ID, "VOID", 1); err != nil {
		t.Fatalf("void: %v", err)
	}
	if live, rev := dbtestutil.LedgerEntries(t, pool, glpost.SourcePayment, p.ID); live != 1 || rev != 1 {
		t.Errorf("entries = %d live, %d reversal; want 1, 1", live, rev)
	}
	if got := dbtestutil.SlotBalance(t, pool, glpost.SlotAR) - ar; !near(got, 0) {
		t.Errorf("AR after post+void moved by %v, want 0", got)
	}
}

// A payment that waits on an approver posts when it is approved, not before.
func TestLedger_PostsOnApprovalNotBefore(t *testing.T) {
	pool := testPool(t)
	configureApprover(t, pool)
	p := gatedPayment(t, pool)
	if live, _ := dbtestutil.LedgerEntries(t, pool, glpost.SourcePayment, p.ID); live != 0 {
		t.Fatalf("a Pending payment has %d entries; want none", live)
	}
	if _, err := Approve(context.Background(), pool, p.ID, 1, false); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if live, _ := dbtestutil.LedgerEntries(t, pool, glpost.SourcePayment, p.ID); live != 1 {
		t.Errorf("entries after approval = %d, want 1", live)
	}
}
