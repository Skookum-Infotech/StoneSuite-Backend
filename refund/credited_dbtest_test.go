//go:build dbtest

package refund

import (
	"context"
	"testing"

	"stonesuite-backend/creditmemo"
)

// Money already turned into a credit memo cannot also be refunded: of a 500
// overpayment with 200 credited, only 300 is left to draw a refund against.
func TestApply_FromPayment_NetsOutCreditMemosIssuedFromIt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, paymentUUID := seedUnappliedPayment(t, pool, 500)
	if _, err := creditmemo.Create(ctx, pool, creditmemo.CreateCreditMemoInput{
		CustomerUUID: custUUID, SourcePaymentUUID: paymentUUID, Amount: 200, Reason: "Excess payment",
	}, 1); err != nil {
		t.Fatalf("seed credit memo from payment: %v", err)
	}
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: firstMethodID(t, pool), Amount: 500}, 1)
	if err != nil {
		t.Fatalf("create refund: %v", err)
	}

	if _, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 400, 1); err == nil {
		t.Fatal("refunding 400 of a 500 payment with 200 already credited must be rejected")
	}
	rf, err = Apply(ctx, pool, rf.ID, paymentUUID, "", 300, 1)
	if err != nil {
		t.Fatalf("refunding the remaining 300: %v", err)
	}
	if rf.AppliedTotal != 300 {
		t.Fatalf("AppliedTotal = %v, want 300", rf.AppliedTotal)
	}
}
