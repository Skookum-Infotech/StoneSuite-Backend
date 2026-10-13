//go:build dbtest

package vendorbill

import (
	"context"
	"errors"
	"testing"
)

func updateInvoiceInput(invoice string) UpdateVendorBillInput {
	return UpdateVendorBillInput{vendorBillFields: vendorBillFields{
		VendorInvoiceNumber: invoice, BillDate: "2026-08-01", Items: []LineInput{freeLine(1)},
	}}
}

func TestUpdate_InvoiceNumberChangeIsDuplicateGuarded(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)
	first, err := Create(ctx, pool, linkedInput(vendor, "", "INV-0042", freeLine(1)), 1)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := Create(ctx, pool, linkedInput(vendor, "", "INV-7", freeLine(1)), 1)
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	// Changing to another bill's number is refused, naming that bill.
	_, err = Update(ctx, pool, second.ID, updateInvoiceInput("inv 42"), 1)
	var d *DuplicateDocumentError
	if !errors.As(err, &d) || d.ExistingUUID != first.ID {
		t.Fatalf("err = %v, want DuplicateDocumentError of %s", err, first.ID)
	}

	// An explicit override goes through and is flagged.
	in := updateInvoiceInput("inv 42")
	in.AllowDuplicate = true
	got, err := Update(ctx, pool, second.ID, in, 1)
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if !got.DuplicateOverride || got.DuplicateOfUUID != first.ID {
		t.Errorf("override = %v of %q, want true of %q", got.DuplicateOverride, got.DuplicateOfUUID, first.ID)
	}

	// Re-saving a bill under its own number never collides with itself.
	if _, err := Update(ctx, pool, first.ID, updateInvoiceInput("INV-0042"), 1); err != nil {
		t.Errorf("unchanged number: %v", err)
	}
	// A reformatting of its own number is not a change either.
	if _, err := Update(ctx, pool, first.ID, updateInvoiceInput("inv 42"), 1); err != nil {
		t.Errorf("reformatted own number: %v", err)
	}
	// A blank number is never guarded.
	if _, err := Update(ctx, pool, first.ID, updateInvoiceInput(""), 1); err != nil {
		t.Errorf("blank number: %v", err)
	}
}
