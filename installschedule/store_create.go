package installschedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	fieldRequestID = "requestId"

	// Fabrication job status codes that close a job to scheduling.
	jobStatusCompleted = "COMP"
	jobStatusCancelled = "CANC"
)

// CreateResult is the outcome of Create.
type CreateResult struct {
	Appointment *Appointment
	HistoryID   int64
	Replayed    bool
}

// Create books an appointment (now is the caller's clock), returning a *ConflictError on unacknowledged overlaps.
func Create(ctx context.Context, pool *pgxpool.Pool, loc *time.Location, now time.Time, in CreateInput, actorEmployeeID int, canSee func(string) bool) (*CreateResult, error) {
	if err := ValidateCreate(in, now.In(loc)); err != nil {
		return nil, err
	}
	if in.RequestID != "" {
		var u pgtype.UUID
		if err := u.Scan(in.RequestID); err != nil {
			return nil, &ValidationError{Field: fieldRequestID, Message: "must be a UUID"}
		}
	}
	iv, err := ResolveInterval(in.StartLocal, in.EndLocal, in.AllDay, loc)
	if err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create appointment: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			slog.Warn("rollback create appointment", "error", rbErr)
		}
	}()

	// The closed-job check deliberately precedes the idempotency replay: a closed job accepts no
	// scheduling activity at all, so a retried request after the job closes reports ErrJobClosed.
	jobID, err := resolveOpenJob(ctx, tx, in.FabricationJobUUID)
	if err != nil {
		return nil, err
	}
	if res, err := replayCreate(ctx, tx, jobID, in.RequestID); err != nil || res != nil {
		return res, err
	}

	employeeIDs := crewIDs(in.Crew)
	if err := validateEmployees(ctx, tx, employeeIDs); err != nil {
		return nil, err
	}
	if err := lockScope(ctx, tx, jobID, employeeIDs); err != nil {
		return nil, err
	}
	// A concurrent retry may have committed while we waited for the locks.
	if res, err := replayCreate(ctx, tx, jobID, in.RequestID); err != nil || res != nil {
		return res, err
	}

	shown, more, all, err := findConflicts(ctx, tx, now, jobID, iv, employeeIDs, 0, canSee)
	if err != nil {
		return nil, err
	}
	if len(all) > 0 && !in.AcknowledgeConflicts {
		return nil, &ConflictError{Conflicts: shown, More: more}
	}

	apptID, uuid, err := insertAppointment(ctx, tx, jobID, in, iv, actorEmployeeID)
	if err != nil {
		return nil, err
	}
	if err := insertCrew(ctx, tx, apptID, in.Crew, actorEmployeeID); err != nil {
		return nil, err
	}
	appt, err := Get(ctx, tx, uuid)
	if err != nil {
		return nil, fmt.Errorf("reload created appointment: %w", err)
	}
	histID, err := writeHistory(ctx, tx, HistoryRow{
		AppointmentID: apptID, Action: ActionCreate,
		ToStart: &iv.Start, ToEnd: &iv.End,
		ConflictsAcknowledged: ackedConflicts(in.AcknowledgeConflicts, all),
		Snapshot:              HistorySnapshot{Appointment: *appt, CrewIDs: employeeIDs},
		ActorEmployeeID:       actorEmployeeID,
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create appointment: %w", err)
	}
	return &CreateResult{Appointment: appt, HistoryID: histID}, nil
}

// ackedConflicts returns the conflicts to persist as acknowledged, or nil when none were overridden.
func ackedConflicts(acknowledged bool, conflicts []Conflict) []Conflict {
	if !acknowledged {
		return nil
	}
	return conflicts
}

// crewIDs extracts the employee ids of a crew list.
func crewIDs(crew []CrewInput) []int {
	ids := make([]int, 0, len(crew))
	for _, c := range crew {
		ids = append(ids, c.EmployeeID)
	}
	return ids
}

// resolveOpenJob returns the internal id of a live job that is not completed or cancelled.
func resolveOpenJob(ctx context.Context, tx pgx.Tx, jobUUID string) (int, error) {
	var u pgtype.UUID
	if err := u.Scan(jobUUID); err != nil {
		return 0, ErrNotFound
	}
	var id int
	var status string
	err := tx.QueryRow(ctx, `
		SELECT fj.fabrication_job_id, s.record_status_code
		FROM fabrication_job fj
		JOIN lkp_record_status s ON s.record_status_id = fj.fabrication_job_status
		WHERE fj.fabrication_job_uuid = $1 AND fj.fabrication_job_deleted_at IS NULL`, jobUUID).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve fabrication job: %w", err)
	}
	if status == jobStatusCompleted || status == jobStatusCancelled {
		return 0, ErrJobClosed
	}
	return id, nil
}

// replayCreate returns the earlier result for a repeated (job, request id), or nil when there is none.
func replayCreate(ctx context.Context, tx pgx.Tx, jobID int, requestID string) (*CreateResult, error) {
	if requestID == "" {
		return nil, nil
	}
	var apptID int
	var uuid string
	err := tx.QueryRow(ctx, `
		SELECT install_appointment_id, install_appointment_uuid::text FROM install_appointment
		WHERE fabrication_job_id = $1 AND create_request_id = $2`, jobID, requestID).Scan(&apptID, &uuid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup create request: %w", err)
	}
	appt, err := Get(ctx, tx, uuid)
	if err != nil {
		return nil, fmt.Errorf("load replayed appointment: %w", err)
	}
	var histID int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MIN(install_appointment_history_id), 0) FROM install_appointment_history
		WHERE install_appointment_id = $1 AND action = $2`, apptID, ActionCreate).Scan(&histID); err != nil {
		return nil, fmt.Errorf("load replayed history id: %w", err)
	}
	return &CreateResult{Appointment: appt, HistoryID: histID, Replayed: true}, nil
}

// validateEmployees checks every id is an active, non-system, non-deleted employee.
func validateEmployees(ctx context.Context, tx pgx.Tx, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	unique := sortedUniqueIDs(ids)
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM employee
		WHERE employee_id = ANY($1) AND employee_is_active AND NOT employee_is_system AND employee_deleted_at IS NULL`,
		unique).Scan(&n); err != nil {
		return fmt.Errorf("validate crew employees: %w", err)
	}
	if n != len(unique) {
		return &ValidationError{Field: fieldCrew, Message: "unknown or inactive employee"}
	}
	return nil
}

// insertAppointment inserts the appointment row and returns its internal id and uuid.
func insertAppointment(ctx context.Context, tx pgx.Tx, jobID int, in CreateInput, iv Interval, actor int) (int, string, error) {
	var requestID *string
	if in.RequestID != "" {
		requestID = &in.RequestID
	}
	var id int
	var uuid string
	err := tx.QueryRow(ctx, `
		INSERT INTO install_appointment
			(fabrication_job_id, visit_type, starts_at, ends_at, all_day, local_start_date, local_end_date,
			 timezone, notes, create_request_id, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11,$11)
		RETURNING install_appointment_id, install_appointment_uuid::text`,
		jobID, string(in.VisitType), iv.Start, iv.End, iv.AllDay, iv.LocalStartDate, iv.LocalEndDate,
		iv.TZ, in.Notes, requestID, actor).Scan(&id, &uuid)
	if err != nil {
		return 0, "", fmt.Errorf("insert appointment: %w", err)
	}
	return id, uuid, nil
}

// insertCrew inserts the crew rows of an appointment.
func insertCrew(ctx context.Context, tx pgx.Tx, apptID int, crew []CrewInput, actor int) error {
	for _, c := range crew {
		if _, err := tx.Exec(ctx, `
			INSERT INTO install_appointment_crew (install_appointment_id, employee_id, crew_role, assigned_by)
			VALUES ($1,$2,$3,$4)`, apptID, c.EmployeeID, string(c.Role), actor); err != nil {
			return fmt.Errorf("insert appointment crew %d: %w", c.EmployeeID, err)
		}
	}
	return nil
}
