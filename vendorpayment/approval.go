// vendorpayment/approval.go — AD-6: the configuration-driven approval gate,
// a near-verbatim port of purchaseorder/approval.go with purchase_order ->
// vendor_payment, PORD -> VPAY. One addition beyond the purchaseorder shape:
// once full approval is reached while the payment is at PAPV, Approve also
// flips the payment's own status to APPV in the same transaction — see
// Approve's doc comment.
package vendorpayment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Approval status values stored in vendor_payment.vendor_payment_approval_status (AD-6).
const (
	approvalNone     = "none"     // no approvers configured for the current status
	approvalPending  = "pending"  // gated: awaiting the required sign-offs
	approvalApproved = "approved" // enough configured approvers have signed off
)

// vendorPaymentRecordTypeCode is the lkp_record_type code for Vendor Payment.
const vendorPaymentRecordTypeCode = "VPAY"

// ErrNotApprover is returned when a caller who is not a configured approver
// for the vendor payment's current status tries to approve it. Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this vendor payment's current status")

// ErrApprovalRequired is returned when a vendor payment is asked to leave a
// status that still requires approval sign-off. Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this vendor payment must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for a
// vendor payment whose current status has no configured approvers. Maps to 409.
var ErrApprovalNotRequired = errors.New("this vendor payment's current status does not require approval")

// activeApproverCount returns how many active approvers are configured for
// the VPAY record type at a status. Zero means no approval gate there.
func activeApproverCount(ctx context.Context, q workflow.Querier, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM vendor_payment_approver
		WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`,
		recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count vendor payment approvers: %w", err)
	}
	return n, nil
}

// signOffCount returns how many distinct approvers have signed off on a
// vendor payment at a status.
func signOffCount(ctx context.Context, q workflow.Querier, vpInternalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM vendor_payment_approval
		WHERE vendor_payment_id = $1 AND record_status_id = $2`,
		vpInternalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count vendor payment approvals: %w", err)
	}
	return n, nil
}

// isConfiguredApprover reports whether an employee is an active configured
// approver for the VPAY record type at a status.
func isConfiguredApprover(ctx context.Context, q workflow.Querier, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM vendor_payment_approver
			WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`,
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check vendor payment approver: %w", err)
	}
	return exists, nil
}

// Approve records one approver's sign-off on a vendor payment at its current
// status (AD-6). Requires the caller to be a configured approver for that
// status, is idempotent per (payment, status, approver), and flips the
// header's approval_status to 'approved' once the sign-off count reaches the
// configured approver count.
//
// Delta from purchaseorder's shape: PAPV->APPV is not reachable through the
// generic Transition endpoint (store_transition.go rejects a manual move).
// Once full approval is reached while the payment is at PAPV, Approve also
// moves vendor_payment_status to APPV in this same transaction and writes a
// second history row recording that move — the only path from PAPV to APPV
// (spec AD-6/§7).
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int) (*VendorPayment, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve vendor payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT vp.vendor_payment_id, vp.vendor_payment_status, rs.record_status_code
		FROM vendor_payment vp
		JOIN lkp_record_status rs ON rs.record_status_id = vp.vendor_payment_status
		WHERE vp.vendor_payment_uuid = $1 AND vp.vendor_payment_deleted_at IS NULL
		FOR UPDATE OF vp`, uuid).Scan(&internalID, &curStatusID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor payment for approval: %w", err)
	}

	recordTypeID, err := typeIDByCode(ctx, pool, vendorPaymentRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VPAY record type: %w", err)
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
		INSERT INTO vendor_payment_approval (vendor_payment_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (vendor_payment_id, record_status_id, approver_employee_id) DO NOTHING`,
		internalID, curStatusID, approverEmployeeID); err != nil {
		return nil, fmt.Errorf("record vendor payment approval: %w", err)
	}

	approved, err := signOffCount(ctx, tx, internalID, curStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalPending
	var approvedBy any
	if approved >= required {
		newApprovalStatus = approvalApproved
		approvedBy = approverEmployeeID
	}
	if _, err := tx.Exec(ctx, `
		UPDATE vendor_payment SET
			vendor_payment_approval_status = $2, vendor_payment_approved_by = $3, vendor_payment_updated_at = NOW()
		WHERE vendor_payment_id = $1`, internalID, newApprovalStatus, approvedBy); err != nil {
		return nil, fmt.Errorf("update vendor payment approval status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_payment_history (vendor_payment_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $2, 'approve', $3)`, internalID, curStatusID, nullableInt(approverEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor payment approve history: %w", err)
	}

	if newApprovalStatus == approvalApproved && curStatusCode == "PAPV" {
		appvStatusID, err := statusIDByCode(ctx, pool, recordTypeID, "APPV")
		if err != nil {
			return nil, fmt.Errorf("resolve APPV status: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE vendor_payment SET vendor_payment_status = $1, vendor_payment_updated_at = NOW(),
				vendor_payment_updated_by = $2, vendor_payment_record_version = vendor_payment_record_version + 1
			WHERE vendor_payment_id = $3`, appvStatusID, nullableInt(approverEmployeeID), internalID); err != nil {
			return nil, fmt.Errorf("move approved vendor payment to APPV: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO vendor_payment_history (vendor_payment_id, from_status_id, to_status_id, action, actor_employee_id)
			VALUES ($1, $2, $3, 'approve', $4)`, internalID, curStatusID, appvStatusID, nullableInt(approverEmployeeID)); err != nil {
			return nil, fmt.Errorf("insert vendor payment PAPV to APPV history: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve vendor payment: %w", err)
	}
	return Get(ctx, pool, uuid)
}
