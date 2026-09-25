//go:build dbtest

package creditmemo

import (
	"context"
	"errors"
	"testing"
)

func floatPtr(v float64) *float64 { return &v }

// A credit memo no longer needs line items: an amount stands in for them, with
// sales tax and adjustment applied on top of it.
func TestCreate_AmountOnly_ComputesTotalsWithoutLines(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)

	cm, err := Create(context.Background(), pool, CreateCreditMemoInput{
		CustomerUUID: custUUID, Amount: 250, SalesTaxPercent: 10, Adjustment: -5,
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cm.Subtotal != 250 || cm.TaxTotal != 25 || cm.GrandTotal != 270 {
		t.Errorf("subtotal/tax/grand = %v/%v/%v, want 250/25/270", cm.Subtotal, cm.TaxTotal, cm.GrandTotal)
	}
	if cm.UnappliedAmount != 270 {
		t.Errorf("UnappliedAmount = %v, want 270", cm.UnappliedAmount)
	}
	if len(cm.Lines) != 0 {
		t.Errorf("an amount-only memo must have no lines, got %d", len(cm.Lines))
	}
}

func TestCreate_AmountRules(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)
	line := []CreditMemoLineInput{{LineNumber: 1, Description: "Freight", Quantity: 1, UnitPrice: 10}}

	tests := []struct {
		name string
		in   CreateCreditMemoInput
	}{
		{"neither an amount nor lines", CreateCreditMemoInput{CustomerUUID: custUUID}},
		{"a zero amount", CreateCreditMemoInput{CustomerUUID: custUUID, Amount: 0}},
		{"a negative amount", CreateCreditMemoInput{CustomerUUID: custUUID, Amount: -5}},
		{"an amount together with lines", CreateCreditMemoInput{CustomerUUID: custUUID, Amount: 10, Lines: line}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Create(context.Background(), pool, tc.in, 1)
			var clientErr ClientError
			if !errors.As(err, &clientErr) {
				t.Fatalf("expected a ClientError, got %T: %v", err, err)
			}
		})
	}
}

func TestCreate_LegacyLinesStillWork(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)

	cm, err := Create(context.Background(), pool, CreateCreditMemoInput{
		CustomerUUID: custUUID,
		Lines:        []CreditMemoLineInput{{LineNumber: 1, Description: "Freight credit", Quantity: 2, UnitPrice: 50}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cm.GrandTotal != 100 || len(cm.Lines) != 1 {
		t.Errorf("grand/lines = %v/%d, want 100/1", cm.GrandTotal, len(cm.Lines))
	}
}

// Recomputing an amount-only memo used to sum its (non-existent) lines and
// zero the totals, so every edit that did not resend the amount must keep it.
func TestUpdate_AmountOnlyMemo_KeepsItsSubtotalWhenTheAmountIsOmitted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	cm, err := Create(ctx, pool, CreateCreditMemoInput{CustomerUUID: custUUID, Amount: 200}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	cm, err = Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Memo: "notes only"}, 1)
	if err != nil {
		t.Fatalf("update notes: %v", err)
	}
	if cm.Subtotal != 200 || cm.GrandTotal != 200 {
		t.Fatalf("after a notes-only edit subtotal/grand = %v/%v, want 200/200", cm.Subtotal, cm.GrandTotal)
	}

	cm, err = Update(ctx, pool, cm.ID, UpdateCreditMemoInput{SalesTaxPercent: floatPtr(10)}, 1)
	if err != nil {
		t.Fatalf("update tax: %v", err)
	}
	if cm.Subtotal != 200 || cm.TaxTotal != 20 || cm.GrandTotal != 220 {
		t.Fatalf("after a tax edit subtotal/tax/grand = %v/%v/%v, want 200/20/220", cm.Subtotal, cm.TaxTotal, cm.GrandTotal)
	}

	cm, err = Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Amount: floatPtr(300)}, 1)
	if err != nil {
		t.Fatalf("update amount: %v", err)
	}
	if cm.Subtotal != 300 || cm.TaxTotal != 30 || cm.GrandTotal != 330 {
		t.Fatalf("after an amount edit subtotal/tax/grand = %v/%v/%v, want 300/30/330", cm.Subtotal, cm.TaxTotal, cm.GrandTotal)
	}
}

func TestUpdate_AmountReplacesLegacyLines(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	cm, err := Create(ctx, pool, CreateCreditMemoInput{
		CustomerUUID: custUUID,
		Lines:        []CreditMemoLineInput{{LineNumber: 1, Description: "Freight credit", Quantity: 2, UnitPrice: 50}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	cm, err = Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Amount: floatPtr(40)}, 1)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(cm.Lines) != 0 || cm.Subtotal != 40 || cm.GrandTotal != 40 {
		t.Fatalf("lines/subtotal/grand = %d/%v/%v, want 0/40/40", len(cm.Lines), cm.Subtotal, cm.GrandTotal)
	}
}

func TestUpdate_AmountRules(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	cm, err := Create(ctx, pool, CreateCreditMemoInput{CustomerUUID: custUUID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	tests := []struct {
		name string
		in   UpdateCreditMemoInput
	}{
		{"a zero amount", UpdateCreditMemoInput{Amount: floatPtr(0)}},
		{"a negative amount", UpdateCreditMemoInput{Amount: floatPtr(-1)}},
		{"an amount together with lines", UpdateCreditMemoInput{
			Amount: floatPtr(10),
			Lines:  []CreditMemoLineInput{{LineNumber: 1, Description: "x", Quantity: 1, UnitPrice: 1}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Update(ctx, pool, cm.ID, tc.in, 1)
			var clientErr ClientError
			if !errors.As(err, &clientErr) {
				t.Fatalf("expected a ClientError, got %T: %v", err, err)
			}
		})
	}
}

func TestUpdate_AmountOnApprovedMemo_IsRejected(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)
	cm := seedApprovedMemo(t, pool, custUUID, 100)

	_, err := Update(context.Background(), pool, cm.ID, UpdateCreditMemoInput{Amount: floatPtr(50)}, 1)
	var clientErr ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("an amount change after approval must be a ClientError, got %T: %v", err, err)
	}
}
