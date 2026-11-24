package automation

import (
	"context"
	"fmt"

	"stonesuite-backend/approvalchain"
)

// vendorBillRecipient is one person responsible for a vendor bill.
type vendorBillRecipient struct {
	UserID     string
	IdentityID string
	Email      string
	Name       string
}

// vendorBillApproversSQL lists the vendor bill approvers who can still act, once each, using the same
// definition of "live" as every other approval query (employee not deleted, user active).
const vendorBillApproversSQL = `
SELECT DISTINCT u.id::text, u.identity_id::text, u.email, u.full_name
FROM vendor_bill_approver ea` + approvalchain.LiveApproverJoin + `
WHERE ea.is_active
  AND ea.record_type_id = (SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VBIL')
ORDER BY u.email`

// vendorBillRecipients says who is told about a bill: its owner when the owner is an active user with an
// email address, otherwise the bill's configured approvers who can still act. A bill with neither has
// nobody to tell and returns an empty list (the planner records it as skipped, so the delivery log says why).
func vendorBillRecipients(ctx context.Context, q Querier, doc VendorBillDoc) ([]vendorBillRecipient, error) {
	if doc.OwnerEmail != "" && doc.OwnerUserID != "" {
		return []vendorBillRecipient{{UserID: doc.OwnerUserID, IdentityID: doc.OwnerIdentityID, Email: doc.OwnerEmail}}, nil
	}
	rows, err := q.Query(ctx, vendorBillApproversSQL)
	if err != nil {
		return nil, fmt.Errorf("vendor bill approvers: %w", err)
	}
	defer rows.Close()
	var out []vendorBillRecipient
	for rows.Next() {
		var r vendorBillRecipient
		if err := rows.Scan(&r.UserID, &r.IdentityID, &r.Email, &r.Name); err != nil {
			return nil, fmt.Errorf("scan vendor bill approver: %w", err)
		}
		if r.Email != "" {
			out = append(out, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vendor bill approvers: %w", err)
	}
	return out, nil
}
