package installschedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChangeResult is the outcome of Update, Cancel or Complete.
type ChangeResult struct {
	Before, After          *Appointment
	HistoryIDs             []int64
	Actions                []string
	AddedCrew, RemovedCrew []int
}

// apptRef is the locked identity and state of one appointment.
type apptRef struct {
	ID, JobID, Version int
	Status             Status
}

// beginTx opens a transaction whose rollback failure (other than after commit) is logged.
func beginTx(ctx context.Context, pool *pgxpool.Pool, what string) (pgx.Tx, func(), error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin %s: %w", what, err)
	}
	return tx, func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			slog.Warn("rollback "+what, "error", rbErr)
		}
	}, nil
}

// readRef reads an appointment's internal ids, status and version; ErrNotFound when absent or its job is deleted.
func readRef(ctx context.Context, q Querier, uuid string) (apptRef, error) {
	var u pgtype.UUID
	if err := u.Scan(uuid); err != nil {
		return apptRef{}, ErrNotFound
	}
	var r apptRef
	err := q.QueryRow(ctx, `
		SELECT ia.install_appointment_id, ia.fabrication_job_id, ia.appointment_status, ia.record_version
		FROM install_appointment ia
		JOIN fabrication_job fj ON fj.fabrication_job_id = ia.fabrication_job_id
		WHERE ia.install_appointment_uuid = $1 AND fj.fabrication_job_deleted_at IS NULL`, uuid).
		Scan(&r.ID, &r.JobID, &r.Status, &r.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return apptRef{}, ErrNotFound
	}
	if err != nil {
		return apptRef{}, fmt.Errorf("read appointment ref: %w", err)
	}
	return r, nil
}

// readCrewIDs returns the employee ids currently assigned to an appointment.
func readCrewIDs(ctx context.Context, q Querier, apptID int) ([]int, error) {
	rows, err := q.Query(ctx, `SELECT employee_id FROM install_appointment_crew WHERE install_appointment_id = $1`, apptID)
	if err != nil {
		return nil, fmt.Errorf("read appointment crew ids: %w", err)
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan appointment crew id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate appointment crew ids: %w", err)
	}
	return ids, nil
}

// lockAppointment takes the advisory locks (job, then old and extra employees ascending) and only then
// re-reads the appointment, so the returned status and version are stable for the rest of the transaction.
// Every Update bumps the version, so a crew change racing the lock phase surfaces as a version mismatch.
func lockAppointment(ctx context.Context, tx pgx.Tx, uuid string, extraEmployeeIDs []int) (apptRef, error) {
	pre, err := readRef(ctx, tx, uuid)
	if err != nil {
		return apptRef{}, err
	}
	oldCrew, err := readCrewIDs(ctx, tx, pre.ID)
	if err != nil {
		return apptRef{}, err
	}
	if err := lockScope(ctx, tx, pre.JobID, append(oldCrew, extraEmployeeIDs...)); err != nil {
		return apptRef{}, err
	}
	fresh, err := readRef(ctx, tx, uuid)
	if err != nil {
		return apptRef{}, err
	}
	lockedCrew, err := readCrewIDs(ctx, tx, fresh.ID)
	if err != nil {
		return apptRef{}, err
	}
	// A crew change that committed between the first read and the locks leaves some employees unlocked.
	if !slices.Equal(sortedUniqueIDs(oldCrew), sortedUniqueIDs(lockedCrew)) {
		return apptRef{}, &StaleVersionError{CurrentVersion: fresh.Version}
	}
	return fresh, nil
}

// checkScheduledAt verifies the appointment is scheduled and at the expected version.
func checkScheduledAt(ref apptRef, expectedVersion int) error {
	if ref.Status != StatusScheduled {
		return ErrInvalidStatus
	}
	if ref.Version != expectedVersion {
		return &StaleVersionError{CurrentVersion: ref.Version}
	}
	return nil
}

// staleAfterMiss builds the stale-version error after a versioned UPDATE matched no row.
func staleAfterMiss(ctx context.Context, q Querier, apptID int) error {
	var v int
	if err := q.QueryRow(ctx, `SELECT record_version FROM install_appointment WHERE install_appointment_id = $1`, apptID).Scan(&v); err != nil {
		return fmt.Errorf("read current appointment version: %w", err)
	}
	return &StaleVersionError{CurrentVersion: v}
}

// snapshotOf builds the history snapshot for a reloaded appointment.
func snapshotOf(a *Appointment) HistorySnapshot {
	ids := make([]int, 0, len(a.Crew))
	for _, m := range a.Crew {
		ids = append(ids, m.EmployeeID)
	}
	return HistorySnapshot{Appointment: *a, CrewIDs: sortedUniqueIDs(ids)}
}
