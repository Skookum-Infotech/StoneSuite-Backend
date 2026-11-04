package installschedule

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update patches a scheduled appointment (now is the caller's clock), returning a *ConflictError on
// unacknowledged overlaps and a *StaleVersionError when in.ExpectedVersion is out of date.
func Update(ctx context.Context, pool *pgxpool.Pool, loc *time.Location, now time.Time, uuid string, in UpdateInput, actorEmployeeID int, canSee func(string) bool) (*ChangeResult, error) {
	var newCrewIDs []int
	if in.Crew != nil {
		if err := ValidateCrew(*in.Crew); err != nil {
			return nil, err
		}
		newCrewIDs = crewIDs(*in.Crew)
	}

	tx, rollback, err := beginTx(ctx, pool, "update appointment")
	if err != nil {
		return nil, err
	}
	defer rollback()

	ref, err := lockAppointment(ctx, tx, uuid, newCrewIDs)
	if err != nil {
		return nil, err
	}
	if err := checkScheduledAt(ref, in.ExpectedVersion); err != nil {
		return nil, err
	}
	before, err := Get(ctx, tx, uuid)
	if err != nil {
		return nil, fmt.Errorf("load appointment before update: %w", err)
	}
	plan, err := planUpdate(before, in, loc, now)
	if err != nil {
		return nil, err
	}
	if plan.timeChanged {
		if err := ensureJobOpen(ctx, tx, ref.JobID); err != nil {
			return nil, err
		}
	}
	if err := validateEmployees(ctx, tx, plan.added); err != nil {
		return nil, err
	}
	var acked []Conflict
	if plan.needsConflictCheck() {
		shown, more, all, err := findConflicts(ctx, tx, now, ref.JobID, plan.iv, plan.conflictEmployees(), ref.ID, canSee)
		if err != nil {
			return nil, err
		}
		if len(all) > 0 && !in.AcknowledgeConflicts {
			return nil, &ConflictError{Conflicts: shown, More: more}
		}
		acked = ackedConflicts(in.AcknowledgeConflicts, all)
	}

	if err := applyUpdate(ctx, tx, ref, plan, in.ExpectedVersion, actorEmployeeID); err != nil {
		return nil, err
	}
	after, err := Get(ctx, tx, uuid)
	if err != nil {
		return nil, fmt.Errorf("reload appointment after update: %w", err)
	}
	res := &ChangeResult{Before: before, After: after, AddedCrew: plan.added, RemovedCrew: plan.removed}
	if err := recordUpdateHistory(ctx, tx, res, ref.ID, plan, in.Reason, acked, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update appointment: %w", err)
	}
	return res, nil
}

// ensureJobOpen returns ErrJobClosed when the job is completed or cancelled.
func ensureJobOpen(ctx context.Context, tx pgx.Tx, jobID int) error {
	var status string
	err := tx.QueryRow(ctx, `
		SELECT s.record_status_code FROM fabrication_job fj
		JOIN lkp_record_status s ON s.record_status_id = fj.fabrication_job_status
		WHERE fj.fabrication_job_id = $1`, jobID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read job status: %w", err)
	}
	if status == jobStatusCompleted || status == jobStatusCancelled {
		return ErrJobClosed
	}
	return nil
}
