package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// resourceVendorBill is automation_deliveries.resource (and the approvalchain workflow key) for vendor bills.
	resourceVendorBill = "vendor_bill"
	vbStatusPaid       = "PAID"
	vbStatusVoid       = "VOID"
	vbStatusInitiated  = "PINI"
	// vendorBillLookbackDays / vendorBillLookaheadDays bound the candidate scan around the due date.
	vendorBillLookbackDays  = 2
	vendorBillLookaheadDays = 3

	reasonNotPayable       = "not_payable"
	reasonPaymentInitiated = "payment_initiated"
)

// vendorBillRemindStatuses are the bill statuses that still owe money with no payment in flight.
// It is vendorbill.PayableStatuses minus PINI, whose balance is already committed.
var vendorBillRemindStatuses = []string{"APPV", "PART", "ODUE"}

// VendorBillDoc is one approved, unpaid vendor bill approaching or past its due date.
// The Owner* fields are empty unless the owning employee resolves to an active user.
type VendorBillDoc struct {
	UUID            string
	Number          string
	VendorName      string
	OwnerEmail      string
	OwnerUserID     string
	OwnerIdentityID string
	DueDate         time.Time
	Balance         float64
	// Route is the in-app route, set by the renderer.
	Route string
}

const vendorBillSelect = `
SELECT vb.vendor_bill_uuid::text, COALESCE(vb.vendor_bill_number, ''), vb.vendor_bill_vendor_name,
       COALESCE(u.email, ''), COALESCE(u.id::text, ''), COALESCE(u.identity_id::text, ''),
       vb.vendor_bill_due_date, rs.record_status_code, (vb.vendor_bill_deleted_at IS NOT NULL),
       COALESCE(u.status, ''),
       (vb.vendor_bill_grand_total - vb.vendor_bill_amount_paid - vb.vendor_bill_credit_total)::float8
FROM vendor_bill vb
JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
LEFT JOIN employee e ON e.employee_id = vb.vendor_bill_owner_id AND e.employee_deleted_at IS NULL
LEFT JOIN users u ON u.id = e.employee_user_id AND u.status = '` + userStatusActive + `'
`

// vendorBillFacts are the live facts vendorBillReason judges.
type vendorBillFacts struct {
	Deleted bool
	Status  string
	Balance float64
	DueDate *time.Time
}

// listVendorBillCandidates returns approved, unpaid, live bills whose due date
// falls within [today-2d, today+3d].
func listVendorBillCandidates(ctx context.Context, q Querier, today time.Time) ([]VendorBillDoc, error) {
	return scanVendorBills(ctx, q, vendorBillSelect+`
WHERE rs.record_status_code = ANY($1::text[])
  AND vb.vendor_bill_due_date BETWEEN $2::date - $3::int AND $2::date + $4::int
  AND vb.vendor_bill_deleted_at IS NULL
  AND (vb.vendor_bill_grand_total - vb.vendor_bill_amount_paid - vb.vendor_bill_credit_total) > 0
ORDER BY vb.vendor_bill_due_date, vb.vendor_bill_id`,
		vendorBillRemindStatuses, today, vendorBillLookbackDays, vendorBillLookaheadDays)
}

// listVendorBillsDueBy returns approved, unpaid, live bills due on or before today+days, overdue
// ones included (the daily digest keeps reporting a bill until it is paid).
func listVendorBillsDueBy(ctx context.Context, q Querier, today time.Time, days int) ([]VendorBillDoc, error) {
	return scanVendorBills(ctx, q, vendorBillSelect+`
WHERE rs.record_status_code = ANY($1::text[])
  AND vb.vendor_bill_due_date <= $2::date + $3::int
  AND vb.vendor_bill_deleted_at IS NULL
  AND (vb.vendor_bill_grand_total - vb.vendor_bill_amount_paid - vb.vendor_bill_credit_total) > 0
ORDER BY vb.vendor_bill_due_date, vb.vendor_bill_id`,
		vendorBillRemindStatuses, today, days)
}

// scanVendorBills runs a query built on vendorBillSelect and decodes every row.
func scanVendorBills(ctx context.Context, q Querier, sql string, args ...any) ([]VendorBillDoc, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list vendor bills: %w", err)
	}
	defer rows.Close()
	var out []VendorBillDoc
	for rows.Next() {
		var d VendorBillDoc
		var f vendorBillFacts
		var ownerStatus string // the owner is judged when recipients are resolved, not here
		if err := rows.Scan(&d.UUID, &d.Number, &d.VendorName, &d.OwnerEmail, &d.OwnerUserID, &d.OwnerIdentityID,
			&f.DueDate, &f.Status, &f.Deleted, &ownerStatus, &d.Balance); err != nil {
			return nil, fmt.Errorf("scan vendor bill: %w", err)
		}
		if f.DueDate != nil {
			d.DueDate = *f.DueDate
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vendor bills: %w", err)
	}
	return out, nil
}

// vendorBillEligible re-checks one bill live at send time. reason "" means eligible.
func vendorBillEligible(ctx context.Context, q Querier, uuid string, plannedDue time.Time) (ok bool, reason string, live VendorBillDoc, err error) {
	var f vendorBillFacts
	var ownerStatus string
	scanErr := q.QueryRow(ctx, vendorBillSelect+` WHERE vb.vendor_bill_uuid = $1::uuid`, uuid).Scan(
		&live.UUID, &live.Number, &live.VendorName, &live.OwnerEmail, &live.OwnerUserID, &live.OwnerIdentityID,
		&f.DueDate, &f.Status, &f.Deleted, &ownerStatus, &live.Balance)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return false, reasonVoided, VendorBillDoc{}, nil
	}
	if scanErr != nil {
		return false, "", VendorBillDoc{}, fmt.Errorf("vendor bill eligibility %s: %w", uuid, scanErr)
	}
	if f.DueDate != nil {
		live.DueDate = *f.DueDate
	}
	f.Balance = live.Balance
	reason = vendorBillReason(f, plannedDue)
	return reason == "", reason, live, nil
}

// vendorBillReason maps live facts to the first matching skip reason ("" = eligible);
// precedence: voided, paid, payment_initiated, not_payable, anchor_changed. Who is told is not part of
// whether the bill is still worth a reminder: that is checked per recipient when the email is built.
func vendorBillReason(f vendorBillFacts, planned time.Time) string {
	switch {
	case f.Deleted || f.Status == vbStatusVoid:
		return reasonVoided
	case f.Status == vbStatusPaid || f.Balance <= 0:
		return reasonPaid
	case f.Status == vbStatusInitiated:
		return reasonPaymentInitiated
	case !containsString(vendorBillRemindStatuses, f.Status):
		return reasonNotPayable
	case f.DueDate == nil || !sameDay(*f.DueDate, planned):
		return reasonAnchorChanged
	}
	return ""
}

// equalFold reports case-insensitive equality of two addresses.
func equalFold(a, b string) bool { return strings.EqualFold(a, b) }
