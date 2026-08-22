// vendorbill/store_transition.go
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// Transition moves a live vendor bill to toStatusCode, validating the move
// against the static transition map (AD-5), row-locking the bill to
// serialize concurrent transitions, enforcing the AD-6 approval gate, and
// writing a history row.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*VendorBill, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT vb.vendor_bill_id, vb.vendor_bill_status, rs.record_status_code, vb.vendor_bill_approval_status
		FROM vendor_bill vb JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL
		FOR UPDATE OF vb`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor bill for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vbilRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VBIL record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// AD-6 approval gate: a vendor bill may not leave a status that has
	// configured approvers until it has been approved. Recalling to draft
	// (-> DRFT) is always allowed -- it is how a submitter withdraws a
	// pending bill for rework without an approver's sign-off (on top of
	// the engine's own always-allowed exits like Void/Cancel/Reject).
	approverTable := moduleConfig().ApproverTable
	if toStatusCode != draftStatusCode {
		requiredHere, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, recordTypeID, curStatusID)
		if err != nil {
			return nil, err
		}
		if err := approvalchain.CheckTransitionGate(requiredHere, approvalStatus, toStatusCode); err != nil {
			return nil, ErrApprovalRequired
		}
	}
	targetApprovers, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalchain.StatusNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalchain.StatusPending
	}

	if _, err := tx.Exec(ctx, `
		UPDATE vendor_bill SET
			vendor_bill_status = $2, vendor_bill_approval_status = $4, vendor_bill_approved_by = NULL,
			vendor_bill_updated_at = NOW(),
			vendor_bill_updated_by = $3, vendor_bill_record_version = vendor_bill_record_version + 1
		WHERE vendor_bill_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition vendor bill: %w", err)
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
