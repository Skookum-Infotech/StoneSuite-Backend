package fabrication

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Approval status values stored in fabrication_job.job_approval_status.
const (
	approvalNone     = "none"
	approvalPending  = "pending"
	approvalApproved = "approved"
)

// ErrNotApprover maps to HTTP 403.
var ErrNotApprover = errors.New("you are not a configured approver for this job's current status")

// ErrApprovalRequired maps to HTTP 409.
var ErrApprovalRequired = errors.New("this job must be approved before it can leave its current status")

// ErrApprovalNotRequired maps to HTTP 409.
var ErrApprovalNotRequired = errors.New("this job's current status does not require approval")

// activeApproverCount returns how many active approvers are configured for the
// FJOB record type at a status. Zero ⇒ no gate (spec §2.7).
func activeApproverCount(ctx context.Context, q querier, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM fabrication_job_approver
		WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`, recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count fabrication approvers: %w", err)
	}
	return n, nil
}

func signOffCount(ctx context.Context, q querier, jobInternalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM fabrication_job_approval
		WHERE fabrication_job_id = $1 AND record_status_id = $2`, jobInternalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count fabrication approvals: %w", err)
	}
	return n, nil
}

func isConfiguredApprover(ctx context.Context, q querier, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM fabrication_job_approver
			WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`,
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check fabrication approver: %w", err)
	}
	return exists, nil
}

// Approve records one approver's sign-off on a job at its current status (the
// TAPV / QCPS gates, spec §2.7). Idempotent per (job, status, approver); flips
// the header to 'approved' once the sign-off count reaches the configured count.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int) (*Job, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	err = tx.QueryRow(ctx, `
		SELECT fabrication_job_id, fabrication_job_status FROM fabrication_job
		WHERE fabrication_job_uuid = $1 AND fabrication_job_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&internalID, &curStatusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load job for approval: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, fjobRecordTypeCode)
	if err != nil {
		return nil, err
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
		INSERT INTO fabrication_job_approval (fabrication_job_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (fabrication_job_id, record_status_id, approver_employee_id) DO NOTHING`,
		internalID, curStatusID, approverEmployeeID); err != nil {
		return nil, fmt.Errorf("record approval: %w", err)
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
		UPDATE fabrication_job SET job_approval_status = $2, job_approved_by = $3, fabrication_job_updated_at = NOW()
		WHERE fabrication_job_id = $1`, internalID, newStatus, approvedBy); err != nil {
		return nil, fmt.Errorf("update approval status: %w", err)
	}
	writeHistory(ctx, tx, internalID, "approve", &curStatusID, &curStatusID, approverEmployeeID)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve: %w", err)
	}
	return Get(ctx, pool, uuid)
}
