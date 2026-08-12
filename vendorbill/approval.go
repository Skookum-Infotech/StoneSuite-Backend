// vendorbill/approval.go — AD-6: the configuration-driven approval gate, an
// exact structural mirror of purchaseorder/approval.go.
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Approval status values stored in vendor_bill.vendor_bill_approval_status (AD-6).
const (
	approvalNone     = "none"     // no approvers configured for the current status
	approvalPending  = "pending"  // gated: awaiting the required sign-offs
	approvalApproved = "approved" // enough configured approvers have signed off
)

// ErrNotApprover is returned when a caller who is not a configured approver
// for the vendor bill's current status tries to approve it. Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this vendor bill's current status")

// ErrApprovalRequired is returned when a vendor bill is asked to leave a
// status that still requires approval sign-off. Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this vendor bill must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for a
// vendor bill whose current status has no configured approvers. Maps to 409.
var ErrApprovalNotRequired = errors.New("this vendor bill's current status does not require approval")

// activeApproverCount returns how many active approvers are configured for
// the VBIL record type at a status. Zero means no approval gate there.
func activeApproverCount(ctx context.Context, q workflow.Querier, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM vendor_bill_approver
		WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`,
		recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count vendor bill approvers: %w", err)
	}
	return n, nil
}

// signOffCount returns how many distinct approvers have signed off on a
// vendor bill at a status.
func signOffCount(ctx context.Context, q workflow.Querier, vbInternalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM vendor_bill_approval
		WHERE vendor_bill_id = $1 AND record_status_id = $2`,
		vbInternalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count vendor bill approvals: %w", err)
	}
	return n, nil
}

// isConfiguredApprover reports whether an employee is an active configured
// approver for the VBIL record type at a status.
func isConfiguredApprover(ctx context.Context, q workflow.Querier, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM vendor_bill_approver
			WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`,
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check vendor bill approver: %w", err)
	}
	return exists, nil
}

// Approve records one approver's sign-off on a vendor bill at its current
// status (AD-6). Requires the caller to be a configured approver for that
// status, is idempotent per (bill, status, approver), and flips the header's
// approval_status to 'approved' once the sign-off count reaches the
// configured approver count.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int) (*VendorBill, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve vendor bill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	err = tx.QueryRow(ctx, `
		SELECT vendor_bill_id, vendor_bill_status FROM vendor_bill
		WHERE vendor_bill_uuid = $1 AND vendor_bill_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&internalID, &curStatusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor bill for approval: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vbilRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VBIL record type: %w", err)
	}

	required, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if required == 0 {
		return nil, ErrApprovalNotRequired
	}

	ok, err := isConfiguredApprover(ctx, tx, recordTypeID, curStatusID, approverEmployeeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotApprover
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_bill_approval (vendor_bill_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (vendor_bill_id, record_status_id, approver_employee_id) DO NOTHING`,
		internalID, curStatusID, approverEmployeeID); err != nil {
		return nil, fmt.Errorf("record vendor bill approval: %w", err)
	}

	approved, err := signOffCount(ctx, tx, internalID, curStatusID)
	if err != nil {
		return nil, err
	}
	newStatus := approvalPending
	var approvedBy any
	if approved >= required {
		newStatus = approvalApproved
		approvedBy = approverEmployeeID
	}
	if _, err := tx.Exec(ctx, `
		UPDATE vendor_bill SET
			vendor_bill_approval_status = $2, vendor_bill_approved_by = $3, vendor_bill_updated_at = NOW()
		WHERE vendor_bill_id = $1`, internalID, newStatus, approvedBy); err != nil {
		return nil, fmt.Errorf("update vendor bill approval status: %w", err)
	}

	writeHistory(ctx, tx, internalID, "approve", &curStatusID, &curStatusID, approverEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve vendor bill: %w", err)
	}
	return Get(ctx, pool, uuid)
}
