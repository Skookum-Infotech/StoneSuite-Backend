package fabrication

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a live job to toStatusCode. It validates against the static
// map, enforces the approval gate at the current status, applies the inventory
// side-effects for the status being entered (consume at CUTG, release spares at
// COMP), and writes a history row — all inside one transaction with the job row
// locked (spec §1, §4). Resume/hold/cancel have their own entry points.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Job, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	st, err := lockJob(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}

	// Hold and resume are not plain transitions.
	if toStatusCode == StatusOnHold {
		return nil, ClientError{Msg: "Use the hold endpoint to place a job on hold."}
	}
	if err := ValidateTransition(st.statusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, fjobRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve FJOB record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// Approval gate: a job cannot leave a status that has configured approvers
	// until it has been approved. Statuses with no approvers never block.
	requiredHere, err := activeApproverCount(ctx, tx, recordTypeID, st.statusID)
	if err != nil {
		return nil, err
	}
	if requiredHere > 0 && st.approvalStatus != approvalApproved {
		return nil, ErrApprovalRequired
	}
	// The status being entered may itself require approval → start it pending.
	targetApprovers, err := activeApproverCount(ctx, tx, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalPending
	}

	// Inventory side-effects of the status being entered.
	switch toStatusCode {
	case StatusCutting:
		if err := consumeSlabs(ctx, tx, st.internalID, actorEmployeeID); err != nil {
			return nil, err
		}
	case StatusCompleted:
		// Release any slab still merely reserved (never cut) — the
		// over-allocation leak (§4.8).
		if err := releaseReservedSlabs(ctx, tx, st.internalID); err != nil {
			return nil, err
		}
		if err := bumpFulfillment(ctx, tx, st.internalID, actorEmployeeID); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE fabrication_job SET
			fabrication_job_status = $2, job_approval_status = $3, job_approved_by = NULL,
			fabrication_job_updated_at = NOW(), fabrication_job_updated_by = $4,
			fabrication_job_record_version = fabrication_job_record_version + 1
		WHERE fabrication_job_id = $1`, st.internalID, toStatusID, newApprovalStatus, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("transition fabrication job: %w", err)
	}

	// QCPD→EDGP rework reopens the edge/polish/rod/QC steps (§5).
	action := "transition"
	if st.statusCode == StatusQCPending && toStatusCode == StatusEdging {
		action = "rework"
		if err := reopenReworkSteps(ctx, tx, st.internalID, actorEmployeeID); err != nil {
			return nil, err
		}
	}

	writeHistory(ctx, tx, st.internalID, action, &st.statusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// Hold places a live, non-terminal job on hold, recording the status it was in
// so Resume can restore it. HOLD→HOLD is rejected (it would overwrite the
// held-from status and strand the job — spec §1.2).
func Hold(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*Job, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin hold: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	st, err := lockJob(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if st.statusCode == StatusOnHold {
		return nil, ClientError{Msg: "This job is already on hold."}
	}
	if IsTerminal(st.statusCode) {
		return nil, ErrInvalidTransition
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, fjobRecordTypeCode)
	if err != nil {
		return nil, err
	}
	holdID, err := statusIDByCode(ctx, tx, recordTypeID, StatusOnHold)
	if err != nil {
		return nil, err
	}
	// Reservations survive a hold (§4.4.3): another job could otherwise take the
	// slab, leaving this one unresumable. No inventory side-effect here.
	if _, err := tx.Exec(ctx, `
		UPDATE fabrication_job SET
			fabrication_job_status = $2, job_held_from_status_id = $3,
			fabrication_job_updated_at = NOW(), fabrication_job_updated_by = $4,
			fabrication_job_record_version = fabrication_job_record_version + 1
		WHERE fabrication_job_id = $1`, st.internalID, holdID, st.statusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("hold fabrication job: %w", err)
	}
	writeHistory(ctx, tx, st.internalID, "hold", &st.statusID, &holdID, actorEmployeeID)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit hold: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// Resume returns a held job to the status stored in job_held_from_status_id.
// The target is never caller-supplied — that would be privilege escalation
// (resume straight past the QC gate, spec §1.2).
func Resume(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*Job, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin resume: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, holdStatusID int
	var heldFromID *int
	err = tx.QueryRow(ctx, `
		SELECT fj.fabrication_job_id, fj.fabrication_job_status, fj.job_held_from_status_id
		FROM fabrication_job fj
		WHERE fj.fabrication_job_uuid = $1 AND fj.fabrication_job_deleted_at IS NULL
		FOR UPDATE OF fj`, uuid).Scan(&internalID, &holdStatusID, &heldFromID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load job for resume: %w", err)
	}
	if heldFromID == nil {
		return nil, ClientError{Msg: "This job is not on hold."}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE fabrication_job SET
			fabrication_job_status = $2, job_held_from_status_id = NULL,
			fabrication_job_updated_at = NOW(), fabrication_job_updated_by = $3,
			fabrication_job_record_version = fabrication_job_record_version + 1
		WHERE fabrication_job_id = $1`, internalID, *heldFromID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("resume fabrication job: %w", err)
	}
	writeHistory(ctx, tx, internalID, "resume", &holdStatusID, heldFromID, actorEmployeeID)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit resume: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// jobState is the locked snapshot the transition paths read.
type jobState struct {
	internalID     int
	statusID       int
	statusCode     string
	approvalStatus string
}

// lockJob row-locks a live job and returns its current status/approval.
func lockJob(ctx context.Context, tx pgx.Tx, uuid string) (*jobState, error) {
	var s jobState
	err := tx.QueryRow(ctx, `
		SELECT fj.fabrication_job_id, fj.fabrication_job_status, rs.record_status_code, fj.job_approval_status
		FROM fabrication_job fj
		JOIN lkp_record_status rs ON rs.record_status_id = fj.fabrication_job_status
		WHERE fj.fabrication_job_uuid = $1 AND fj.fabrication_job_deleted_at IS NULL
		FOR UPDATE OF fj`, uuid).Scan(&s.internalID, &s.statusID, &s.statusCode, &s.approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock fabrication job: %w", err)
	}
	return &s, nil
}

// reopenReworkSteps resets the edge/polish/rod/QC steps to pending on a QCPD→EDGP
// rework, so the backward move actually records new work (§5).
func reopenReworkSteps(ctx context.Context, tx pgx.Tx, jobInternalID, actorEmployeeID int) error {
	_, err := tx.Exec(ctx, `
		UPDATE fabrication_job_step SET
			step_status = 'pending', step_completed_at = NULL, step_completed_by = NULL
		WHERE fabrication_job_id = $1 AND step_code = ANY($2)`,
		jobInternalID, reworkStepCodes)
	if err != nil {
		return fmt.Errorf("reopen rework steps: %w", err)
	}
	return nil
}

// bumpFulfillment raises line_fulfilled_quantity on the sales-order lines linked
// to this job's pieces when it completes, clamped at the ordered quantity. A
// clamp is logged (fulfillment_clamped) so over-reporting across multiple jobs
// on one line is auditable, never silent (§4.7).
func bumpFulfillment(ctx context.Context, tx pgx.Tx, jobInternalID, actorEmployeeID int) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT fi.sales_order_item_id
		FROM fabrication_job_item fi
		WHERE fi.fabrication_job_id = $1 AND fi.sales_order_item_id IS NOT NULL AND fi.item_deleted_at IS NULL`,
		jobInternalID)
	if err != nil {
		return fmt.Errorf("load linked lines: %w", err)
	}
	var lineIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		lineIDs = append(lineIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, lineID := range lineIDs {
		// Each completed piece fulfills one unit of its line, clamped at quantity.
		var qty, fulfilled float64
		if err := tx.QueryRow(ctx, `
			SELECT quantity, line_fulfilled_quantity FROM sales_order_item
			WHERE sales_order_item_id = $1 FOR UPDATE`, lineID).Scan(&qty, &fulfilled); err != nil {
			return fmt.Errorf("lock sales order line: %w", err)
		}
		want := fulfilled + 1
		applied := want
		if applied > qty {
			applied = qty
			writeHistory(ctx, tx, jobInternalID, "fulfillment_clamped", nil, nil, actorEmployeeID)
		}
		if applied != fulfilled {
			if _, err := tx.Exec(ctx, `
				UPDATE sales_order_item SET line_fulfilled_quantity = $2 WHERE sales_order_item_id = $1`,
				lineID, applied); err != nil {
				return fmt.Errorf("bump line fulfillment: %w", err)
			}
		}
	}
	return nil
}
