package installschedule

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Cancel cancels a scheduled appointment; reason is required (at most MaxReason characters).
func Cancel(ctx context.Context, pool *pgxpool.Pool, uuid string, expectedVersion int, reason string, actorEmployeeID int) (*ChangeResult, error) {
	if reason == "" {
		return nil, &ValidationError{Field: fieldReason, Message: "required"}
	}
	if err := ValidateReason(reason); err != nil {
		return nil, err
	}
	return transition(ctx, pool, uuid, expectedVersion, ActionCancel, `
		UPDATE install_appointment
		SET appointment_status = 'cancelled', cancelled_at = now(), cancelled_by = $3, cancel_reason = $4,
		    updated_at = now(), updated_by = $3, record_version = record_version + 1
		WHERE install_appointment_id = $1 AND record_version = $2 AND appointment_status = 'scheduled'`,
		actorEmployeeID, reason, nil)
}

// Complete marks a scheduled appointment completed once now has reached its start (now is the caller's clock).
func Complete(ctx context.Context, pool *pgxpool.Pool, uuid string, expectedVersion int, actorEmployeeID int, now time.Time) (*ChangeResult, error) {
	return transition(ctx, pool, uuid, expectedVersion, ActionComplete, `
		UPDATE install_appointment
		SET appointment_status = 'completed', completed_at = $4, completed_by = $3,
		    updated_at = now(), updated_by = $3, record_version = record_version + 1
		WHERE install_appointment_id = $1 AND record_version = $2 AND appointment_status = 'scheduled'`,
		actorEmployeeID, "", &now)
}

// transition runs a cancel or complete UPDATE under the appointment's advisory locks and records its history row.
// Arguments after $2 are $3 actor, $4 reason (cancel) or completion instant (complete).
func transition(ctx context.Context, pool *pgxpool.Pool, uuid string, expectedVersion int, action, updateSQL string, actor int, reason string, completedAt *time.Time) (*ChangeResult, error) {
	tx, rollback, err := beginTx(ctx, pool, action+" appointment")
	if err != nil {
		return nil, err
	}
	defer rollback()

	ref, err := lockAppointment(ctx, tx, uuid, nil)
	if err != nil {
		return nil, err
	}
	if err := checkScheduledAt(ref, expectedVersion); err != nil {
		return nil, err
	}
	before, err := Get(ctx, tx, uuid)
	if err != nil {
		return nil, fmt.Errorf("load appointment before %s: %w", action, err)
	}
	if completedAt != nil && completedAt.Before(before.StartsAt) {
		return nil, ErrNotStarted
	}

	var arg any = reason
	if completedAt != nil {
		arg = *completedAt
	}
	tag, err := tx.Exec(ctx, updateSQL, ref.ID, expectedVersion, actor, arg)
	if err != nil {
		return nil, fmt.Errorf("%s appointment: %w", action, err)
	}
	if tag.RowsAffected() == 0 {
		return nil, staleAfterMiss(ctx, tx, ref.ID)
	}
	after, err := Get(ctx, tx, uuid)
	if err != nil {
		return nil, fmt.Errorf("reload appointment after %s: %w", action, err)
	}
	histID, err := writeHistory(ctx, tx, HistoryRow{
		AppointmentID: ref.ID, Action: action,
		FromStart: &before.StartsAt, FromEnd: &before.EndsAt, ToStart: &after.StartsAt, ToEnd: &after.EndsAt,
		Reason: reason, Snapshot: snapshotOf(after), ActorEmployeeID: actor,
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit %s appointment: %w", action, err)
	}
	return &ChangeResult{Before: before, After: after, HistoryIDs: []int64{histID}, Actions: []string{action}}, nil
}
