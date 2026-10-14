package bintransfer

// store_transition.go — moving a transfer through its statuses, and Complete,
// the one move that actually puts stone in a different bin.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/inventory"
)

// Transition moves a transfer to toStatusCode: submit, revise, send back,
// approve by hand (only when nobody is configured to) or cancel. Completed is
// not reachable here -- it moves stone, so it goes through Complete.
//
// note is kept as the cancel reason when cancelling and ignored otherwise.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode, note string, actorEmployeeID int) (*BinTransfer, error) {
	if toStatusCode == StatusCompleted {
		return nil, ClientError{Msg: "Complete a bin transfer with the Complete action; it is what moves the slabs."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if err := ValidateTransition(cur.statusCode, toStatusCode); err != nil {
		return nil, err
	}
	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, err
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	cfg := moduleConfig()
	// AD-8 approval gate: a transfer may not leave a status that has configured
	// approvers until it has been approved, except by an always-allowed exit.
	requiredHere, err := approvalchain.ActiveApproverCount(ctx, tx, cfg.ApproverTable, recordTypeID, cur.statusID)
	if err != nil {
		return nil, err
	}
	if err := approvalchain.CheckTransitionGate(requiredHere, cur.approvalStatus, toStatusCode); err != nil {
		return nil, ErrApprovalRequired
	}
	targetApprovers, err := approvalchain.ActiveApproverCount(ctx, tx, cfg.ApproverTable, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalchain.StatusNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalchain.StatusPending
	}
	// Submitting with nobody configured to approve skips straight to Approved
	// rather than parking the transfer on a "Pending Approval" nobody can resolve.
	if toStatusCode == StatusPending && targetApprovers == 0 {
		toStatusCode = StatusApproved
		if toStatusID, err = statusIDByCode(ctx, tx, recordTypeID, toStatusCode); err != nil {
			return nil, fmt.Errorf("resolve %s status: %w", toStatusCode, err)
		}
		if targetApprovers, err = approvalchain.ActiveApproverCount(ctx, tx, cfg.ApproverTable, recordTypeID, toStatusID); err != nil {
			return nil, err
		}
		newApprovalStatus = approvalchain.StatusApproved
		if targetApprovers > 0 {
			newApprovalStatus = approvalchain.StatusPending
		}
	}

	// Sign-offs belong to the version of the document they were given to. A move
	// back to Draft or onto the gate starts a fresh round, so earlier signatures
	// must not count towards it.
	if toStatusCode == StatusDraft || toStatusCode == StatusPending {
		if _, err := tx.Exec(ctx, `DELETE FROM bin_transfer_approval WHERE bin_transfer_id = $1`, cur.id); err != nil {
			return nil, fmt.Errorf("clear bin transfer sign-offs: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE bin_transfer SET
			bin_transfer_status = $2, bin_transfer_approval_status = $3, bin_transfer_approved_by = NULL,
			bin_transfer_updated_at = NOW(), bin_transfer_updated_by = $4,
			bin_transfer_record_version = bin_transfer_record_version + 1
		WHERE bin_transfer_id = $1`,
		cur.id, toStatusID, newApprovalStatus, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("transition bin transfer: %w", err)
	}
	if toStatusCode == StatusCancelled {
		if _, err := tx.Exec(ctx, `
			UPDATE bin_transfer SET bin_transfer_cancelled_at = NOW(), bin_transfer_cancelled_by = $2,
				bin_transfer_cancel_reason = $3
			WHERE bin_transfer_id = $1`, cur.id, nullableInt(actorEmployeeID), note); err != nil {
			return nil, fmt.Errorf("record bin transfer cancellation: %w", err)
		}
	}
	if err := writeHistory(ctx, tx, cur.id, "transition", &cur.statusID, &toStatusID, actorEmployeeID); err != nil {
		return nil, err
	}
	// Any move takes the transfer out of the state a Reject left it in.
	if err := approvalchain.ClearRejection(ctx, tx, recordTypeID, cur.id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	notifyTransition(ctx, pool, uuid, cur.id, recordTypeID, toStatusID, toStatusCode,
		requiredHere, cur.approvalStatus, targetApprovers, actorEmployeeID)
	return Get(ctx, pool, uuid)
}

// notifyTransition best-effort-notifies approvers when the transfer just entered
// a gated status, or the owner when a gated-unapproved transfer escaped through
// an always-allowed exit (cancel) instead of clearing approval.
func notifyTransition(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, recordTypeID, toStatusID int,
	toStatusCode string, requiredHere int, approvalStatus string, targetApprovers, actorEmployeeID int) {
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

// completionLine is one line as Complete needs it.
type completionLine struct {
	id        int
	slabUUID  string
	serial    string
	toBinID   int
	toBinUUID string
	toBinPath string
	available bool
}

func loadCompletionLines(ctx context.Context, tx pgx.Tx, transferID int) ([]completionLine, error) {
	// Slab id order is the one global lock order, so two completions (or a
	// completion and a bundle move) can never lock the same slabs in opposite
	// orders and deadlock.
	rows, err := tx.Query(ctx, `
		SELECT l.bin_transfer_line_id, s.inventory_slab_uuid, s.slab_serial,
		       l.to_bin_id, tb.inventory_bin_uuid::text, tb.bin_path,
		       (tb.bin_is_active AND tb.bin_deleted_at IS NULL)
		FROM bin_transfer_line l
		JOIN inventory_slab s ON s.inventory_slab_id = l.inventory_slab_id
		JOIN inventory_bin tb ON tb.inventory_bin_id = l.to_bin_id
		WHERE l.bin_transfer_id = $1 AND l.line_deleted_at IS NULL
		ORDER BY l.inventory_slab_id`, transferID)
	if err != nil {
		return nil, fmt.Errorf("load bin transfer lines to complete: %w", err)
	}
	defer rows.Close()
	var out []completionLine
	for rows.Next() {
		var l completionLine
		if err := rows.Scan(&l.id, &l.slabUUID, &l.serial, &l.toBinID, &l.toBinUUID, &l.toBinPath, &l.available); err != nil {
			return nil, fmt.Errorf("scan bin transfer line to complete: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// wrapMoveError names the slab that blocked a completion, so the user knows
// which one to deal with.
func wrapMoveError(serial string, err error) error {
	var ce inventory.ClientError
	if errors.As(err, &ce) {
		return ClientError{Msg: fmt.Sprintf("Slab %s: %s", serial, ce.Msg)}
	}
	if errors.Is(err, inventory.ErrNotFound) {
		return ClientError{Msg: fmt.Sprintf("Slab %s no longer exists.", serial)}
	}
	return fmt.Errorf("move slab %s: %w", serial, err)
}

// Complete applies an approved transfer: every slab moves to its destination bin
// in one transaction, each move logged on the slab and tagged with this document.
// It is all-or-nothing -- if any slab cannot move, none do, and the error names it.
//
// A transfer is completable from Approved, or straight from Draft when nobody is
// configured to approve (the checkpoint has nothing to wait for).
//
// The slab's state is re-read under a row lock here, never trusted from the
// draft: it may have been cut, scrapped, bundled or moved since. A slab already
// in its destination is left alone; one that wandered elsewhere is still sent to
// the destination that was approved.
func Complete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*BinTransfer, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin complete bin transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, err
	}
	switch cur.statusCode {
	case StatusApproved:
	case StatusDraft, StatusPending:
		// Only a checkpoint nobody is configured on may be passed through.
		ungated, err := approvalchain.UngatedGates(ctx, tx, moduleConfig(), recordTypeID)
		if err != nil {
			return nil, err
		}
		if cur.statusCode == StatusPending {
			return nil, ErrApprovalRequired
		}
		if _, ok := approvalchain.PassThroughGate(cur.statusCode, StatusCompleted, NextStatuses, ungated); !ok {
			return nil, ErrApprovalRequired
		}
	default:
		return nil, ErrInvalidTransition
	}

	lines, err := loadCompletionLines(ctx, tx, cur.id)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, ClientError{Msg: "A bin transfer needs at least one slab."}
	}
	for _, l := range lines {
		unit, err := inventory.ResolveUnitForDocument(ctx, tx, l.slabUUID, true)
		if err != nil {
			return nil, wrapMoveError(l.serial, err)
		}
		if !l.available {
			return nil, ClientError{Msg: fmt.Sprintf("Slab %s: bin %s is no longer available.", l.serial, l.toBinPath)}
		}
		if lineOutcome(unit.BinID, l.toBinID) == outcomeMove {
			if err := inventory.MoveUnitForBinTransferTx(ctx, tx, l.slabUUID, l.toBinUUID, "", cur.id, actorEmployeeID); err != nil {
				return nil, wrapMoveError(l.serial, err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE bin_transfer_line SET line_moved_at = NOW() WHERE bin_transfer_line_id = $1`, l.id); err != nil {
			return nil, fmt.Errorf("mark bin transfer line moved: %w", err)
		}
	}

	completedStatusID, err := statusIDByCode(ctx, tx, recordTypeID, StatusCompleted)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bin_transfer SET
			bin_transfer_status = $2, bin_transfer_completed_at = NOW(), bin_transfer_completed_by = $3,
			bin_transfer_updated_at = NOW(), bin_transfer_updated_by = $3,
			bin_transfer_record_version = bin_transfer_record_version + 1
		WHERE bin_transfer_id = $1`, cur.id, completedStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("complete bin transfer: %w", err)
	}
	if err := writeHistory(ctx, tx, cur.id, "complete", &cur.statusID, &completedStatusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := approvalchain.ClearRejection(ctx, tx, recordTypeID, cur.id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit complete bin transfer: %w", err)
	}
	return Get(ctx, pool, uuid)
}
