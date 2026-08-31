package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/workflow"
)

// Transition moves an invoice to toStatusCode after validating the move against
// the static transition map. The invoice row is locked for the rest of the
// transaction so concurrent transitions serialize.
func Transition(ctx context.Context, pool *pgxpool.Pool, id, toStatusCode string, actorEmployeeID int) (*Invoice, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID, typeID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT i.invoice_id, i.invoice_status, i.record_type, rs.record_status_code, i.invoice_approval_status
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL
		FOR UPDATE OF i`, id,
	).Scan(&internalID, &curStatusID, &typeID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve invoice for transition: %w", err)
	}

	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}
	if curStatusCode == "DRFT" && toStatusCode == "PAPV" {
		has, err := workflow.HasAttachments(ctx, tx, id)
		if err != nil {
			return nil, fmt.Errorf("check attachments: %w", err)
		}
		if !has {
			return nil, ErrAttachmentRequired
		}
	}

	toStatusID, err := statusIDByCode(ctx, pool, typeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status: " + toStatusCode}
	}

	// AD-8 approval gate: an invoice may not leave a status that has
	// configured approvers until it has been approved (except into an
	// always-allowed exit like Void -- approvalchain.AlwaysAllowedExitCodes).
	approverTable := moduleConfig().ApproverTable
	requiredHere, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, typeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if err := approvalchain.CheckTransitionGate(requiredHere, approvalStatus, toStatusCode); err != nil {
		return nil, ErrApprovalRequired
	}
	targetApprovers, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, typeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalchain.StatusNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalchain.StatusPending
	}

	if _, err := tx.Exec(ctx, `
		UPDATE invoice SET invoice_status = $1, invoice_approval_status = $2, invoice_approved_by = NULL,
			invoice_updated_at = NOW(), invoice_updated_by = $3, invoice_record_version = invoice_record_version + 1
		WHERE invoice_id = $4`, toStatusID, newApprovalStatus, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update invoice status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO invoice_history (invoice_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'transition', $4)`, internalID, curStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert invoice transition history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	notifyTransition(ctx, pool, id, internalID, typeID, toStatusID, toStatusCode, requiredHere, approvalStatus, targetApprovers, actorEmployeeID)
	return Get(ctx, pool, id)
}

// notifyTransition best-effort-notifies approvers/owner around a manual
// status move (AD-8): approvers when the record just entered a gated
// status, or the owner when a gated-unapproved record escaped via an
// always-allowed exit (void/cancel/reject) instead of clearing approval.
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
