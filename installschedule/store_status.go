package installschedule

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// execTransition runs the versioned status UPDATE for appointment id at version ver.
type execTransition func(ctx context.Context, tx pgx.Tx, id, ver int) (pgconn.CommandTag, error)

// Cancel cancels a scheduled appointment; reason is required (non-blank, at most MaxReason characters).
func Cancel(ctx context.Context, pool *pgxpool.Pool, uuid string, expectedVersion int, reason string, actorEmployeeID int, actorUserID string) (*ChangeResult, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, &ValidationError{Field: fieldReason, Message: "required"}
	}
	if err := ValidateReason(reason); err != nil {
		return nil, err
	}
	exec := func(ctx context.Context, tx pgx.Tx, id, ver int) (pgconn.CommandTag, error) {
		return tx.Exec(ctx, `
			UPDATE install_appointment
			SET appointment_status = 'cancelled', cancelled_at = now(), cancelled_by = $3, cancel_reason = $4,
			    updated_at = now(), updated_by = $3, record_version = record_version + 1
			WHERE install_appointment_id = $1 AND record_version = $2 AND appointment_status = 'scheduled'`,
			id, ver, actorEmployeeID, reason)
	}
	return transition(ctx, pool, uuid, expectedVersion, ActionCancel, reason, actorEmployeeID, actorUserID, nil, exec)
}

// Complete marks a scheduled appointment completed once now has reached its start (now is the caller's clock).
func Complete(ctx context.Context, pool *pgxpool.Pool, uuid string, expectedVersion int, actorEmployeeID int, now time.Time, actorUserID string) (*ChangeResult, error) {
	exec := func(ctx context.Context, tx pgx.Tx, id, ver int) (pgconn.CommandTag, error) {
		return tx.Exec(ctx, `
			UPDATE install_appointment
			SET appointment_status = 'completed', completed_at = $4, completed_by = $3,
			    updated_at = now(), updated_by = $3, record_version = record_version + 1
			WHERE install_appointment_id = $1 AND record_version = $2 AND appointment_status = 'scheduled'`,
			id, ver, actorEmployeeID, now)
	}
	started := func(a *Appointment) error {
		if now.Before(a.StartsAt) {
			return ErrNotStarted
		}
		return nil
	}
	return transition(ctx, pool, uuid, expectedVersion, ActionComplete, "", actorEmployeeID, actorUserID, started, exec)
}

// transition runs a cancel or complete under the appointment's advisory locks and records its history row.
// precheck, when set, vetoes the change after the status and version checks.
func transition(ctx context.Context, pool *pgxpool.Pool, uuid string, expectedVersion int, action, reason string, actor int, actorUserID string, precheck func(*Appointment) error, exec execTransition) (*ChangeResult, error) {
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
	if precheck != nil {
		if err := precheck(before); err != nil {
			return nil, err
		}
	}
	tag, err := exec(ctx, tx, ref.ID, expectedVersion)
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
	res := &ChangeResult{Before: before, After: after, HistoryIDs: []int64{histID}, Actions: []string{action}}
	if err := emitChangeEvents(ctx, tx, res, actorUserID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit %s appointment: %w", action, err)
	}
	return res, nil
}
