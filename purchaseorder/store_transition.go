// purchaseorder/store_transition.go
package purchaseorder

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a live purchase order to toStatusCode, validating the move
// against the static transition map (AD-5), row-locking the order to
// serialize concurrent transitions, enforcing the AD-6 approval gate, and
// writing a history row.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*PurchaseOrder, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT po.purchase_order_id, po.purchase_order_status, rs.record_status_code, po.purchase_order_approval_status
		FROM purchase_order po JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE po.purchase_order_uuid = $1 AND po.purchase_order_deleted_at IS NULL
		FOR UPDATE OF po`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load purchase order for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, pordRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve PORD record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// AD-6 approval gate: a purchase order may not leave a status that has
	// configured approvers until it has been approved. Recalling to draft
	// (→ DRFT) is always allowed — it is how a submitter withdraws a pending
	// order for rework without an approver's sign-off.
	if toStatusCode != draftStatusCode {
		requiredHere, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
		if err != nil {
			return nil, err
		}
		if requiredHere > 0 && approvalStatus != approvalApproved {
			return nil, ErrApprovalRequired
		}
	}
	targetApprovers, err := activeApproverCount(ctx, tx, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalPending
	}

	if _, err := tx.Exec(ctx, `
		UPDATE purchase_order SET
			purchase_order_status = $2, purchase_order_approval_status = $4, purchase_order_approved_by = NULL,
			purchase_order_updated_at = NOW(),
			purchase_order_updated_by = $3, purchase_order_record_version = purchase_order_record_version + 1
		WHERE purchase_order_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition purchase order: %w", err)
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
