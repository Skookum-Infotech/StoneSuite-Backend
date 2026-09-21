// approvalchain/reject.go
package approvalchain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// MaxRejectionReasonLength caps the reason an approver types when rejecting.
const MaxRejectionReasonLength = 1000

var (
	// ErrReasonRequired: a Reject needs a non-blank reason. Maps to 400.
	ErrReasonRequired = errors.New("a rejection reason is required")
	// ErrReasonTooLong: the reason exceeds MaxRejectionReasonLength. Maps to 400.
	ErrReasonTooLong = errors.New("the rejection reason is too long")
	// ErrAlreadyRejected: the record was already rejected and must be edited
	// and resubmitted before it can be approved or rejected again. Maps to 409.
	ErrAlreadyRejected = errors.New("this record was rejected; edit it to resubmit it for approval")
	// ErrRejectNotSupported: the record's gate takes no Reject action. Maps to 409.
	ErrRejectNotSupported = errors.New("this record's current status does not support rejection")
)

// RejectOutcome reports what Reject did, before the caller reloads its own
// typed record via its module's Get.
type RejectOutcome struct {
	InternalID           int    // the record's internal id, for a caller's own notification hook
	ReturnedToStatusCode string // the status the record was sent back to; "" when flagged rejected in place
	Reason               string // the trimmed reason, for a caller's own notification hook
}

// RejectionInfo is the latest rejection recorded against a record.
type RejectionInfo struct {
	ByName string    `json:"byName"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// Reject vetoes a record that is awaiting approval, on behalf of one of its
// configured approvers or a super admin. Unlike Approve it is a veto, not a
// vote: one rejection is enough, and it needs no quorum. What happens next is
// the gate's RejectMode -- the record is sent back to Gate.RejectStatusCode
// (RejectToStatus) or keeps its status with its approval flagged rejected
// until it is edited and resubmitted (RejectInPlace). Either way this round's
// sign-offs are cleared, so a resubmission starts a fresh round rather than
// riding on approvals given to an earlier version of the record, and the
// rejecter and reason are kept (see RejectionInfo) and written to history as
// "reject".
func Reject(ctx context.Context, pool *pgxpool.Pool, cfg ModuleConfig, uuid string, actorEmployeeID int, callerIsSuperAdmin bool, reason string) (RejectOutcome, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return RejectOutcome{}, ErrReasonRequired
	}
	if utf8.RuneCountInString(reason) > MaxRejectionReasonLength {
		return RejectOutcome{}, ErrReasonTooLong
	}

	rec := cfg.Record
	tx, err := pool.Begin(ctx)
	if err != nil {
		return RejectOutcome{}, fmt.Errorf("begin reject %s: %w", rec.Table, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var approvalStatus string
	err = tx.QueryRow(ctx, fmt.Sprintf(
		`SELECT %s, %s, %s FROM %s WHERE %s = $1 AND %s IS NULL FOR UPDATE`,
		rec.IDColumn, rec.StatusColumn, rec.ApprovalStatusColumn, rec.Table, rec.UUIDColumn, rec.DeletedAtColumn,
	), uuid).Scan(&internalID, &curStatusID, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return RejectOutcome{}, ErrNotFound
	}
	if err != nil {
		return RejectOutcome{}, fmt.Errorf("load %s for reject: %w", rec.Table, err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, cfg.RecordTypeCode)
	if err != nil {
		return RejectOutcome{}, err
	}
	curStatusCode, err := statusCodeByID(ctx, tx, curStatusID)
	if err != nil {
		return RejectOutcome{}, err
	}
	gate, gated := cfg.GateFor(curStatusCode)
	if !gated {
		return RejectOutcome{}, ErrApprovalNotRequired
	}
	if gate.Reject == RejectUnsupported {
		return RejectOutcome{}, ErrRejectNotSupported
	}
	required, err := activeApproverCount(ctx, tx, cfg.ApproverTable, recordTypeID, curStatusID)
	if err != nil {
		return RejectOutcome{}, err
	}
	if required == 0 {
		return RejectOutcome{}, ErrApprovalNotRequired
	}
	switch approvalStatus {
	case StatusRejected:
		return RejectOutcome{}, ErrAlreadyRejected
	case StatusApproved:
		return RejectOutcome{}, ErrApprovalNotRequired
	}
	isApprover, err := isConfiguredApprover(ctx, tx, cfg.ApproverTable, recordTypeID, curStatusID, actorEmployeeID)
	if err != nil {
		return RejectOutcome{}, err
	}
	if !isApprover && !callerIsSuperAdmin {
		return RejectOutcome{}, ErrNotApprover
	}

	toStatusID, newApprovalStatus, returnedTo := curStatusID, StatusRejected, ""
	if gate.Reject == RejectToStatus {
		toStatusID, _, err = statusIDAndLabelByCode(ctx, tx, recordTypeID, gate.RejectStatusCode)
		if err != nil {
			return RejectOutcome{}, fmt.Errorf("resolve %s status: %w", gate.RejectStatusCode, err)
		}
		newApprovalStatus, returnedTo = StatusNone, gate.RejectStatusCode
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE %s = $1 AND record_status_id = $2`, cfg.ApprovalTable, rec.IDColumn),
		internalID, curStatusID); err != nil {
		return RejectOutcome{}, fmt.Errorf("clear %s sign-offs: %w", rec.Table, err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET %s = $2, %s = $3, %s = NULL, %s = NOW(), %s = $4, %s = %s + 1 WHERE %s = $1`,
		rec.Table, rec.StatusColumn, rec.ApprovalStatusColumn, rec.ApprovedByColumn,
		rec.UpdatedAtColumn, rec.UpdatedByColumn, rec.RecordVersionColumn, rec.RecordVersionColumn, rec.IDColumn,
	), internalID, toStatusID, newApprovalStatus, nullIntOrNil(actorEmployeeID)); err != nil {
		return RejectOutcome{}, fmt.Errorf("reject %s: %w", rec.Table, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO approval_rejection (record_type_id, record_id, record_status_id, rejected_by, reason)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (record_type_id, record_id) DO UPDATE SET
			record_status_id = EXCLUDED.record_status_id, rejected_by = EXCLUDED.rejected_by,
			rejected_at = CURRENT_TIMESTAMP, reason = EXCLUDED.reason`,
		recordTypeID, internalID, toStatusID, nullIntOrNil(actorEmployeeID), reason); err != nil {
		return RejectOutcome{}, fmt.Errorf("record %s rejection: %w", rec.Table, err)
	}
	if err := writeGenericHistory(ctx, tx, rec, internalID, "reject", &curStatusID, &toStatusID, actorEmployeeID); err != nil {
		return RejectOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RejectOutcome{}, fmt.Errorf("commit reject %s: %w", rec.Table, err)
	}

	NotifyApprovalRejected(ctx, pool, EventContext{
		Table: rec.Table, IDColumn: rec.IDColumn, NumberColumn: rec.NumberColumn, OwnerColumn: rec.OwnerColumn,
		InternalID: internalID, ActorEmployeeID: actorEmployeeID,
		Resource: cfg.Resource, DisplayName: cfg.DisplayName, RecordUUID: uuid, Detail: reason,
	})
	return RejectOutcome{InternalID: internalID, ReturnedToStatusCode: returnedTo, Reason: reason}, nil
}

// CurrentRejection returns the rejection recorded against a record, but only
// while the record still sits in the status that rejection left it in -- a
// stale row must never paint a red banner on a record that has moved on.
// Returns nil when there is none.
func CurrentRejection(ctx context.Context, q workflow.Querier, recordTypeID, internalID, curStatusID int) (*RejectionInfo, error) {
	var r RejectionInfo
	err := q.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(u.full_name, ''), u.email, ''), ar.reason, ar.rejected_at
		FROM approval_rejection ar
		LEFT JOIN employee e ON e.employee_id = ar.rejected_by
		LEFT JOIN users u ON u.id = e.employee_user_id
		WHERE ar.record_type_id = $1 AND ar.record_id = $2 AND ar.record_status_id = $3`,
		recordTypeID, internalID, curStatusID).Scan(&r.ByName, &r.Reason, &r.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load rejection: %w", err)
	}
	return &r, nil
}

// ClearRejection forgets a record's stored rejection. A module's own
// Transition calls it after moving a record (a Draft record sent back by a
// Reject is submitted again, say), and Approve calls it once a round
// completes, so the rejection never outlives the round it belonged to.
func ClearRejection(ctx context.Context, q workflow.Querier, recordTypeID, internalID int) error {
	if _, err := q.Exec(ctx,
		`DELETE FROM approval_rejection WHERE record_type_id = $1 AND record_id = $2`, recordTypeID, internalID); err != nil {
		return fmt.Errorf("clear rejection: %w", err)
	}
	return nil
}

// Resubmission describes a rejected record that ResubmitAfterEdit just
// reopened, so the caller can notify approvers once its own transaction has
// committed.
type Resubmission struct {
	NeedsApproval bool // approvers are still configured, so the record is pending again
	cfg           ModuleConfig
	recordTypeID  int
	statusID      int
	internalID    int
}

// ResubmitAfterEdit reopens approval for a record that was rejected in place
// once its editor has saved a change. Call it inside the module's own Update
// transaction. Returns nil for a record that was not rejected.
func ResubmitAfterEdit(ctx context.Context, tx pgx.Tx, cfg ModuleConfig, internalID int) (*Resubmission, error) {
	rec := cfg.Record
	var curStatusID int
	var approvalStatus string
	if err := tx.QueryRow(ctx, fmt.Sprintf(
		`SELECT %s, %s FROM %s WHERE %s = $1 FOR UPDATE`,
		rec.StatusColumn, rec.ApprovalStatusColumn, rec.Table, rec.IDColumn,
	), internalID).Scan(&curStatusID, &approvalStatus); err != nil {
		return nil, fmt.Errorf("load %s for resubmit: %w", rec.Table, err)
	}
	if approvalStatus != StatusRejected {
		return nil, nil
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, cfg.RecordTypeCode)
	if err != nil {
		return nil, err
	}
	required, err := activeApproverCount(ctx, tx, cfg.ApproverTable, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	// If every approver was removed while the record sat rejected, nothing is
	// left to approve it: it simply stops being gated.
	next := StatusNone
	if required > 0 {
		next = StatusPending
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET %s = $2 WHERE %s = $1`, rec.Table, rec.ApprovalStatusColumn, rec.IDColumn),
		internalID, next); err != nil {
		return nil, fmt.Errorf("resubmit %s: %w", rec.Table, err)
	}
	if err := ClearRejection(ctx, tx, recordTypeID, internalID); err != nil {
		return nil, err
	}
	return &Resubmission{
		NeedsApproval: required > 0, cfg: cfg, recordTypeID: recordTypeID, statusID: curStatusID, internalID: internalID,
	}, nil
}

// Notify best-effort-notifies the approvers that a resubmitted record needs
// their sign-off again. Safe on a nil Resubmission, and a no-op when nothing
// needs approval -- the caller's own Update only has to call it after its
// transaction committed.
func (r *Resubmission) Notify(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) {
	if r == nil || !r.NeedsApproval {
		return
	}
	rec := r.cfg.Record
	NotifyApprovalRequested(ctx, pool, EventContext{
		Table: rec.Table, IDColumn: rec.IDColumn, NumberColumn: rec.NumberColumn,
		ApproverTable: r.cfg.ApproverTable, RecordTypeID: r.recordTypeID, StatusID: r.statusID,
		InternalID: r.internalID, ActorEmployeeID: actorEmployeeID,
		Resource: r.cfg.Resource, DisplayName: r.cfg.DisplayName, RecordUUID: uuid,
	})
}
