// quote/store_transition.go
package quote

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// Transition moves a live quote to toStatusCode, validating the move
// against the static transition map (spec §7), row-locking the quote to
// serialize concurrent transitions, and writing a history row.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Quote, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT qt.quote_id, qt.quote_status, rs.record_status_code, qt.quote_approval_status
		FROM quote qt JOIN lkp_record_status rs ON rs.record_status_id = qt.quote_status
		WHERE qt.quote_uuid = $1 AND qt.quote_deleted_at IS NULL
		FOR UPDATE OF qt`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load quote for transition: %w", err)
	}
	if toStatusCode == "CONV" {
		return nil, ClientError{Msg: "CONV is not a valid manual transition target."}
	}
	recordTypeID, err := recordTypeIDByCode(ctx, tx, quotRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve QUOT record type: %w", err)
	}
	// The static map has the first word on legality, not the last: with
	// nobody configured to approve, a move may pass straight through the
	// approval checkpoint (Draft -> Sent) -- exactly the moves the status
	// control offers via NextStatusCodes, so the two can't disagree.
	var viaGate *approvalchain.Gate
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		ungated, uerr := approvalchain.UngatedGates(ctx, tx, moduleConfig(), recordTypeID)
		if uerr != nil {
			return nil, uerr
		}
		gate, ok := approvalchain.PassThroughGate(curStatusCode, toStatusCode, NextStatuses, ungated)
		if !ok {
			return nil, err
		}
		viaGate = &gate
	}

	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// AD-8 approval gate: an quote may not leave a status that has
	// configured approvers until it has been approved.
	requiredHere, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if requiredHere > 0 && approvalStatus != approvalApproved && !approvalchain.AlwaysAllowedExitCodes[toStatusCode] {
		return nil, ErrApprovalRequired
	}
	targetApprovers, err := activeApproverCount(ctx, tx, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalPending
	}
	// A move onto the approval checkpoint with zero approvers configured
	// skips straight to Approved instead of parking the quote on a "Pending
	// Approval" label nobody can ever resolve -- mirrors finalizeApproval's
	// own floor (approvalApproved) for landing on the gate's approved target.
	if toStatusCode == approvalGateStatusCode && targetApprovers == 0 {
		toStatusCode = approvedStatusCode
		toStatusID, err = statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
		if err != nil {
			return nil, fmt.Errorf("resolve %s status: %w", toStatusCode, err)
		}
		targetApprovers, err = activeApproverCount(ctx, tx, recordTypeID, toStatusID)
		if err != nil {
			return nil, err
		}
		newApprovalStatus = approvalApproved
		if targetApprovers > 0 {
			newApprovalStatus = approvalPending
		}
	}
	// Passing through the unconfigured checkpoint onto its approved target is
	// the same outcome as the auto-skip above -- record it as approved too,
	// so the two paths can't disagree about what landing on APPV means.
	if viaGate != nil && toStatusCode == viaGate.TargetStatusCode && targetApprovers == 0 {
		newApprovalStatus = approvalApproved
	}

	if _, err := tx.Exec(ctx, `
		UPDATE quote SET
			quote_status = $2, quote_approval_status = $4, quote_approved_by = NULL,
			quote_updated_at = NOW(),
			quote_updated_by = $3, quote_record_version = quote_record_version + 1
		WHERE quote_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition quote: %w", err)
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)
	// Any move takes the quote out of the state a Reject left it in.
	if err := approvalchain.ClearRejection(ctx, tx, recordTypeID, internalID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	notifyTransition(ctx, pool, uuid, internalID, recordTypeID, toStatusID, toStatusCode, requiredHere, approvalStatus, targetApprovers, actorEmployeeID)
	return Get(ctx, pool, uuid)
}
