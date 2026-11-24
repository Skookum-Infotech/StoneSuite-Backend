//go:build dbtest

package expense

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorbill"
)

// newDraftClaim creates a one-line claim for the system employee.
func newDraftClaim(t *testing.T, pool *pgxpool.Pool, amount float64) *Expense {
	t.Helper()
	created, err := Create(context.Background(), pool, CreateExpenseInput{expenseFields: expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: amount, Description: "Trip"}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return created
}

// forceApproved moves a claim to Approved without raising its bill, the state a
// claim approved before bills were automatic is in.
func forceApproved(t *testing.T, pool *pgxpool.Pool, uuid string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE expense SET expense_status = (
			SELECT rs.record_status_id FROM lkp_record_status rs
			JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
			WHERE rt.record_type_code = 'EXPN' AND rs.record_status_code = 'APPV')
		WHERE expense_uuid = $1`, uuid); err != nil {
		t.Fatalf("force approved: %v", err)
	}
}

func voidBill(t *testing.T, pool *pgxpool.Pool, billUUID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE vendor_bill SET vendor_bill_status = (
			SELECT rs.record_status_id FROM lkp_record_status rs
			JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
			WHERE rt.record_type_code = 'VBIL' AND rs.record_status_code = 'VOID')
		WHERE vendor_bill_uuid = $1`, billUUID); err != nil {
		t.Fatalf("void bill: %v", err)
	}
}

// An approved claim with no bill can be billed on demand, only once, and again
// after its bill is voided.
func TestCreateBill_OnDemand_OnceAndAgainAfterVoid(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	draft := newDraftClaim(t, pool, 40)
	if _, err := CreateBill(ctx, pool, draft.ID, 1); !IsClientError(err) {
		t.Fatalf("CreateBill on a draft = %v, want a ClientError", err)
	}

	forceApproved(t, pool, draft.ID)
	billed, err := CreateBill(ctx, pool, draft.ID, 1)
	if err != nil {
		t.Fatalf("CreateBill: %v", err)
	}
	if billed.StatusCode != "APPV" || billed.VendorBill == nil || billed.VendorBill.StatusCode != "DRFT" {
		t.Fatalf("after CreateBill: status %q bill %+v, want Approved with a Draft bill", billed.StatusCode, billed.VendorBill)
	}
	firstBill := billed.VendorBill.ID

	if _, err := CreateBill(ctx, pool, draft.ID, 1); !IsClientError(err) {
		t.Fatalf("second CreateBill = %v, want a ClientError (already billed)", err)
	}

	voidBill(t, pool, firstBill)
	again, err := CreateBill(ctx, pool, draft.ID, 1)
	if err != nil {
		t.Fatalf("CreateBill after void: %v", err)
	}
	if again.VendorBill == nil || again.VendorBill.ID == firstBill {
		t.Fatalf("CreateBill after void = %+v, want a new bill", again.VendorBill)
	}
	old, err := vendorbill.Get(ctx, pool, firstBill)
	if err != nil {
		t.Fatalf("load voided bill: %v", err)
	}
	if old.Expense != nil {
		t.Errorf("the voided bill still points at the claim: %+v", old.Expense)
	}
}

// Billable lists a vendor's approved, unbilled claims -- and the Vendor Bill form
// can raise a bill for one by hand, once, to the right vendor only.
func TestBillable_AndManualBillFromClaim(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	claim := newDraftClaim(t, pool, 55)
	forceApproved(t, pool, claim.ID)

	// The system employee's vendor appears once a claim has been billed; bill a
	// first claim to bring it into being, then use a second for the manual path.
	seed := newDraftClaim(t, pool, 5)
	forceApproved(t, pool, seed.ID)
	if _, err := CreateBill(ctx, pool, seed.ID, 1); err != nil {
		t.Fatalf("seed CreateBill: %v", err)
	}
	var vendorUUID string
	if err := pool.QueryRow(ctx, `SELECT vendor_uuid FROM vendor WHERE vendor_employee_id = 1 AND vendor_deleted_at IS NULL`).Scan(&vendorUUID); err != nil {
		t.Fatalf("load the employee vendor: %v", err)
	}

	list, err := Billable(ctx, pool, string(authz.ScopeAll), "", vendorUUID, "")
	if err != nil {
		t.Fatalf("Billable: %v", err)
	}
	found, billedListed := false, false
	for _, c := range list {
		found = found || c.ID == claim.ID
		billedListed = billedListed || c.ID == seed.ID
	}
	if !found || billedListed {
		t.Fatalf("Billable = %+v, want the unbilled claim and not the billed one", list)
	}
	if other, err := Billable(ctx, pool, string(authz.ScopeAll), "", "00000000-0000-0000-0000-000000000000", ""); err != nil || len(other) != 0 {
		t.Fatalf("Billable for another vendor = %+v (%v), want none", other, err)
	}

	in := vendorbill.CreateVendorBillInput{VendorUUID: vendorUUID, ExpenseUUID: claim.ID}
	in.Items = []vendorbill.LineInput{{LineNumber: 1, Description: "Trip", Quantity: 1, UnitPrice: 55}}
	bill, err := vendorbill.Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("vendorbill.Create from claim: %v", err)
	}
	if bill.Expense == nil || bill.Expense.ID != claim.ID {
		t.Fatalf("bill.Expense = %+v, want the claim", bill.Expense)
	}
	if _, err := vendorbill.Create(ctx, pool, in, 1); err == nil {
		t.Fatal("a second bill for the same claim was accepted")
	}

	// A bill for a claim must be to the claimant's own vendor, and not also a PO bill.
	var otherVendor string
	if err := pool.QueryRow(ctx, `SELECT vendor_uuid FROM vendor WHERE vendor_employee_id IS NULL AND vendor_deleted_at IS NULL LIMIT 1`).Scan(&otherVendor); err == nil {
		wrong := vendorbill.CreateVendorBillInput{VendorUUID: otherVendor, ExpenseUUID: seed.ID}
		wrong.Items = in.Items
		if _, err := vendorbill.Create(ctx, pool, wrong, 1); err == nil {
			t.Error("a claim was billed to somebody else's vendor")
		}
	}
}
