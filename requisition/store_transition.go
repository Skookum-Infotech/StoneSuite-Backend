// requisition/store_transition.go
package requisition

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// Transition moves a live requisition to toStatusCode, validating the move
// against the static transition map (AD-6), row-locking the requisition to
// serialize concurrent transitions, enforcing the AD-7 approval gate, and
// writing a history row.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Requisition, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT reqn.requisition_id, reqn.requisition_status, rs.record_status_code, reqn.requisition_approval_status
		FROM requisition reqn JOIN lkp_record_status rs ON rs.record_status_id = reqn.requisition_status
		WHERE reqn.requisition_uuid = $1 AND reqn.requisition_deleted_at IS NULL
		FOR UPDATE OF reqn`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load requisition for transition: %w", err)
	}
	recordTypeID, err := recordTypeIDByCode(ctx, tx, reqnRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve REQN record type: %w", err)
	}
	// The static map has the first word on legality, not the last: with
	// nobody configured to approve, a move may pass straight through the
	// approval checkpoint (Draft -> Approved in one step) -- exactly the
	// moves the status control offers via NextStatusCodes, so the two can't
	// disagree.
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

	// AD-7 approval gate: a requisition may not leave a status that has
	// configured approvers until it has been approved. Recalling to draft
	// (→ DRFT) is always allowed — it is how a submitter withdraws a pending
	// request for rework without an approver's sign-off (on top of the
	// engine's own always-allowed exits like Void/Cancel/Reject).
	approverTable := moduleConfig().ApproverTable
	var requiredHere int
	if toStatusCode != draftStatusCode {
		requiredHere, err = approvalchain.ActiveApproverCount(ctx, tx, approverTable, recordTypeID, curStatusID)
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
	// A move onto an approval checkpoint with zero approvers configured
	// skips straight to the gate's approved target instead of parking the
	// requisition on a status literally labeled "Pending Approval" that
	// nothing is actually pending on -- mirrors engine.finalize's own floor
	// (StatusApproved) for landing on that target.
	if gate, gated := moduleConfig().GateFor(toStatusCode); gated && targetApprovers == 0 {
		toStatusCode = gate.TargetStatusCode
		toStatusID, err = statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
		if err != nil {
			return nil, fmt.Errorf("resolve %s status: %w", toStatusCode, err)
		}
		targetApprovers, err = approvalchain.ActiveApproverCount(ctx, tx, approverTable, recordTypeID, toStatusID)
		if err != nil {
			return nil, err
		}
		newApprovalStatus = approvalchain.StatusApproved
		if targetApprovers > 0 {
			newApprovalStatus = approvalchain.StatusPending
		}
	}
	// Passing through the unconfigured checkpoint onto its approved target is
	// the same outcome as the auto-skip above -- record it as approved too,
	// so the two paths can't disagree about what landing on APPV means.
	if viaGate != nil && toStatusCode == viaGate.TargetStatusCode && targetApprovers == 0 {
		newApprovalStatus = approvalchain.StatusApproved
	}

	if _, err := tx.Exec(ctx, `
		UPDATE requisition SET
			requisition_status = $2, requisition_approval_status = $4, requisition_approved_by = NULL,
			requisition_updated_at = NOW(),
			requisition_updated_by = $3, requisition_record_version = requisition_record_version + 1
		WHERE requisition_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition requisition: %w", err)
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)
	// Any move takes the requisition out of the state a Reject left it in.
	if err := approvalchain.ClearRejection(ctx, tx, recordTypeID, internalID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	notifyTransition(ctx, pool, uuid, internalID, recordTypeID, toStatusID, toStatusCode, requiredHere, approvalStatus, targetApprovers, actorEmployeeID)
	return Get(ctx, pool, uuid)
}

// notifyTransition best-effort-notifies approvers/owner around a manual
// status move (AD-7): approvers when the requisition just entered a gated
// status, or the requester when a gated-unapproved requisition escaped via
// an always-allowed exit (void/cancel/reject) instead of clearing approval.
func notifyTransition(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, recordTypeID, toStatusID int, toStatusCode string, requiredHere int, approvalStatus string, targetApprovers int, actorEmployeeID int) {
	cfg := moduleConfig()
	rec := cfg.Record
	if targetApprovers > 0 {
		approvalchain.NotifyApprovalRequested(ctx, pool, approvalchain.EventContext{
			Table: rec.Table, IDColumn: rec.IDColumn, NumberColumn: rec.NumberColumn,
			ApproverTable: cfg.ApproverTable, RecordTypeID: recordTypeID, StatusID: toStatusID,
			InternalID: internalID, ActorEmployeeID: actorEmployeeID,
			Resource: cfg.Resource, DisplayName: cfg.DisplayName, RecordUUID: uuid,
		})
	}
	if requiredHere > 0 && approvalStatus != approvalchain.StatusApproved && approvalchain.AlwaysAllowedExitCodes[toStatusCode] {
		approvalchain.NotifyApprovalRejected(ctx, pool, approvalchain.EventContext{
			Table: rec.Table, IDColumn: rec.IDColumn, NumberColumn: rec.NumberColumn, OwnerColumn: rec.OwnerColumn,
			InternalID: internalID, ActorEmployeeID: actorEmployeeID,
			Resource: cfg.Resource, DisplayName: cfg.DisplayName, RecordUUID: uuid,
		})
	}
}

// notifyCreated best-effort-notifies the actor who created a new requisition
// that the creation succeeded.
func notifyCreated(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, actorEmployeeID int) {
	cfg := moduleConfig()
	approvalchain.NotifyCreated(ctx, pool, approvalchain.EventContext{
		Table: cfg.Record.Table, IDColumn: cfg.Record.IDColumn, NumberColumn: cfg.Record.NumberColumn,
		InternalID: internalID, ActorEmployeeID: actorEmployeeID,
		Resource: cfg.Resource, DisplayName: cfg.DisplayName, RecordUUID: uuid,
	})
}
