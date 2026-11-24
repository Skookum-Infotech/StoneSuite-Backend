//go:build dbtest

package expense

import (
	"context"
	"testing"

	"stonesuite-backend/vendorbill"
)

// An approved claim raises a Draft vendor bill from its lines, billed by the
// claimant's Employee vendor; paying that bill marks the claim Paid.
func TestApproval_RaisesVendorBill_AndPaymentMarksClaimPaid(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields: expenseFields{
		Memo: "Site visit",
		Items: []LineInput{
			{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 100, Description: "Taxi"},
			{LineNumber: 2, CategoryCode: "MEALS", ExpenseDate: "2026-08-10", Amount: 25.50},
		},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.VendorBill != nil {
		t.Fatal("a draft claim must not have a bill yet")
	}

	// No approver is configured, so submitting lands straight on Approved.
	approved, err := Transition(ctx, pool, created.ID, "SUBM", 1)
	if err != nil {
		t.Fatalf("Transition to SUBM: %v", err)
	}
	if approved.StatusCode != "APPV" {
		t.Fatalf("StatusCode = %q, want APPV", approved.StatusCode)
	}
	if approved.VendorBill == nil || approved.VendorBill.StatusCode != "DRFT" {
		t.Fatalf("VendorBill = %+v, want a Draft bill", approved.VendorBill)
	}

	bill, err := vendorbill.Get(ctx, pool, approved.VendorBill.ID)
	if err != nil {
		t.Fatalf("vendorbill.Get: %v", err)
	}
	if bill.GrandTotal != 125.5 || len(bill.Items) != 2 {
		t.Errorf("bill total/lines = %v/%d, want 125.5/2", bill.GrandTotal, len(bill.Items))
	}
	if bill.VendorInvoiceNumber != approved.Number {
		t.Errorf("bill invoice number = %q, want the claim number %q", bill.VendorInvoiceNumber, approved.Number)
	}
	if bill.Expense == nil || bill.Expense.ID != created.ID {
		t.Errorf("bill.Expense = %+v, want a link back to the claim", bill.Expense)
	}

	// Approve the bill, pay it in full: the claim follows.
	if _, err := pool.Exec(ctx, `
		UPDATE vendor_bill SET vendor_bill_status = (
			SELECT rs.record_status_id FROM lkp_record_status rs
			JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
			WHERE rt.record_type_code = 'VBIL' AND rs.record_status_code = 'APPV')
		WHERE vendor_bill_uuid = $1`, bill.ID); err != nil {
		t.Fatalf("approve bill: %v", err)
	}
	if _, err := vendorbill.RecordPayment(ctx, pool, bill.ID, vendorbill.RecordPaymentInput{Amount: 125.5}, 1); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}
	paid, err := Get(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if paid.StatusCode != "PAID" {
		t.Errorf("claim StatusCode = %q after the bill was paid, want PAID", paid.StatusCode)
	}
	if paid.VendorBill == nil || paid.VendorBill.StatusCode != "PAID" {
		t.Errorf("claim VendorBill = %+v, want a Paid bill", paid.VendorBill)
	}
}
