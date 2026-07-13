//go:build dbtest

package payment

import (
	"context"
	"testing"
)

func TestUpdate_NonMonetaryFieldsOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := Update(ctx, pool, p.ID, UpdatePaymentInput{MethodID: methodID, ReferenceNumber: "Check #99", Memo: "updated"}, 1)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ReferenceNumber != "Check #99" || updated.Amount != 100 {
		t.Fatalf("expected reference updated and amount unchanged, got %+v", updated)
	}
}

func TestSoftDelete_BlockedWithLiveApplications(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 50, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := SoftDelete(ctx, pool, p.ID, 1); err == nil {
		t.Fatal("expected delete to be blocked while a live application exists")
	}
	if _, err := Unapply(ctx, pool, p.ID, invUUID, 1); err != nil {
		t.Fatalf("unapply: %v", err)
	}
	if err := SoftDelete(ctx, pool, p.ID, 1); err != nil {
		t.Fatalf("expected delete to succeed once unapplied: %v", err)
	}
	if _, err := Get(ctx, pool, p.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
