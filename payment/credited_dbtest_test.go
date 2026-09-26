//go:build dbtest

package payment

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/creditmemo"
)

// seedCreditedPayment creates a 500 payment (nothing applied) plus a sent 500
// invoice for the same customer, then issues a credit memo of `credited`
// against the payment's overpayment.
func seedCreditedPayment(t *testing.T, pool *pgxpool.Pool, credited float64) (paymentUUID, invUUID, memoUUID string) {
	t.Helper()
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 500)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: firstMethodID(t, pool), Amount: 500}, 1)
	if err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	cm, err := creditmemo.Create(ctx, pool, creditmemo.CreateCreditMemoInput{
		CustomerUUID: custUUID, SourcePaymentUUID: p.ID, Amount: credited, Reason: "Excess payment",
	}, 1)
	if err != nil {
		t.Fatalf("seed credit memo from payment: %v", err)
	}
	return p.ID, invUUID, cm.ID
}

// The 200 already turned into a credit memo is no longer the payment's to
// spend, so it can only be applied to an invoice up to the remaining 300.
func TestApply_CapsAtUnappliedNetOfCreditMemos(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	paymentUUID, invUUID, _ := seedCreditedPayment(t, pool, 200)

	_, err := Apply(ctx, pool, paymentUUID, invUUID, 400, 1)
	var clientErr ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("applying money already credited must be a ClientError, got %T: %v", err, err)
	}

	p, err := Apply(ctx, pool, paymentUUID, invUUID, 300, 1)
	if err != nil {
		t.Fatalf("applying the remaining 300: %v", err)
	}
	if p.AppliedTotal != 300 {
		t.Fatalf("AppliedTotal = %v, want 300", p.AppliedTotal)
	}
}

func TestGet_ExposesTheCreditedTotal(t *testing.T) {
	pool := testPool(t)
	paymentUUID, _, _ := seedCreditedPayment(t, pool, 200)

	p, err := Get(context.Background(), pool, paymentUUID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.CreditedTotal != 200 {
		t.Fatalf("CreditedTotal = %v, want 200", p.CreditedTotal)
	}
}

func TestTransition_Void_IsBlockedWhileCreditMemosAreLinked(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	paymentUUID, _, memoUUID := seedCreditedPayment(t, pool, 200)

	_, err := Transition(ctx, pool, paymentUUID, "VOID", 1)
	var clientErr ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("voiding a payment with a live credit memo must be a ClientError, got %T: %v", err, err)
	}

	if _, err := creditmemo.Transition(ctx, pool, memoUUID, "VOID", 1); err != nil {
		t.Fatalf("void the credit memo: %v", err)
	}
	if _, err := Transition(ctx, pool, paymentUUID, "VOID", 1); err != nil {
		t.Fatalf("voiding once the credit memo is void: %v", err)
	}
}

func TestSoftDelete_IsBlockedWhileCreditMemosAreLinked(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	paymentUUID, _, memoUUID := seedCreditedPayment(t, pool, 200)

	err := SoftDelete(ctx, pool, paymentUUID, 1)
	var clientErr ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("deleting a payment with a live credit memo must be a ClientError, got %T: %v", err, err)
	}

	if err := creditmemo.SoftDelete(ctx, pool, memoUUID, 1); err != nil {
		t.Fatalf("delete the credit memo: %v", err)
	}
	if err := SoftDelete(ctx, pool, paymentUUID, 1); err != nil {
		t.Fatalf("deleting once the credit memo is gone: %v", err)
	}
}
