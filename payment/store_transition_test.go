//go:build dbtest

package payment

import (
	"context"
	"testing"

	"stonesuite-backend/invoice"
)

func TestTransition_HappyPath(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p, err = Transition(ctx, pool, p.ID, "APPV", 1)
	if err != nil {
		t.Fatalf("transition to APPV: %v", err)
	}
	if p.StatusCode != "APPV" {
		t.Fatalf("expected APPV, got %s", p.StatusCode)
	}
}

func TestTransition_VoidCascadesUnapply(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 100, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	p2, err := Transition(ctx, pool, p.ID, "VOID", 1)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if p2.StatusCode != "VOID" || p2.AppliedTotal != 0 || p2.UnappliedAmount != 100 {
		t.Fatalf("expected voided/0/100, got status=%s applied=%v unapplied=%v", p2.StatusCode, p2.AppliedTotal, p2.UnappliedAmount)
	}
	inv, _ := invoice.Get(ctx, pool, invUUID)
	if inv.StatusCode != "SENT" || inv.AmountPaid != 0 {
		t.Fatalf("expected invoice reverted to SENT/0, got status=%s paid=%v", inv.StatusCode, inv.AmountPaid)
	}
	// A voided payment can no longer be applied.
	if _, err := Apply(ctx, pool, p.ID, invUUID, 10, 1); err == nil {
		t.Fatal("expected error applying a voided payment")
	}
}
