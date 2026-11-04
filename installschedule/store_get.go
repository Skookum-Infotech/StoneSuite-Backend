package installschedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// layoutLocalOut is the wall-clock layout returned to clients.
const layoutLocalOut = "2006-01-02T15:04"

// Querier is the subset of pgxpool.Pool and pgx.Tx the read paths need.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// apptSelect joins an appointment to its job and customer for response building.
const apptSelect = `
	SELECT ia.install_appointment_id, ia.install_appointment_uuid::text, fj.fabrication_job_uuid::text,
	       COALESCE(fj.fabrication_job_number,''), c.customer_name, fj.job_site_addr_city,
	       ia.visit_type, ia.appointment_status, ia.starts_at, ia.ends_at, ia.timezone, ia.all_day,
	       ia.local_start_date, ia.local_end_date, COALESCE(ia.notes,''), ia.cancel_reason, ia.record_version,
	       (js.record_status_code = '` + jobStatusCancelled + `')
	FROM install_appointment ia
	JOIN fabrication_job fj ON fj.fabrication_job_id = ia.fabrication_job_id
	JOIN lkp_record_status js ON js.record_status_id = fj.fabrication_job_status
	JOIN customer c ON c.customer_id = fj.fabrication_job_customer_id`

// scanAppt scans one apptSelect row, returning the internal id alongside the appointment.
func scanAppt(row pgx.Row) (int, *Appointment, error) {
	var a Appointment
	var id int
	if err := row.Scan(&id, &a.ID, &a.FabricationJobID, &a.JobNumber, &a.CustomerName, &a.SiteCity,
		&a.VisitType, &a.Status, &a.StartsAt, &a.EndsAt, &a.Timezone, &a.AllDay,
		&a.LocalStartDate, &a.LocalEndDate, &a.Notes, &a.CancelReason, &a.Version, &a.JobCancelled); err != nil {
		return 0, nil, err
	}
	a.Crew = []CrewMember{}
	fillLocal(&a)
	return id, &a, nil
}

// fillLocal sets StartLocal/EndLocal from the stored instants in the row's own timezone.
func fillLocal(a *Appointment) {
	loc, err := time.LoadLocation(a.Timezone)
	if err != nil {
		slog.Warn("install appointment has unknown timezone; formatting in UTC", "timezone", a.Timezone, "appointment", a.ID, "error", err)
		loc = time.UTC
	}
	a.StartLocal = a.StartsAt.In(loc).Format(layoutLocalOut)
	a.EndLocal = a.EndsAt.In(loc).Format(layoutLocalOut)
}

// loadCrew attaches crew members to the appointments keyed by internal id.
func loadCrew(ctx context.Context, q Querier, byID map[int]*Appointment) error {
	if len(byID) == 0 {
		return nil
	}
	ids := make([]int, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	rows, err := q.Query(ctx, `
		SELECT iac.install_appointment_id, e.employee_id,
		       btrim(e.employee_first_name || ' ' || e.employee_last_name), iac.crew_role
		FROM install_appointment_crew iac
		JOIN employee e ON e.employee_id = iac.employee_id
		WHERE iac.install_appointment_id = ANY($1)
		ORDER BY iac.install_appointment_id, (iac.crew_role = 'lead') DESC, e.employee_id`, ids)
	if err != nil {
		return fmt.Errorf("load appointment crew: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var m CrewMember
		if err := rows.Scan(&id, &m.EmployeeID, &m.Name, &m.Role); err != nil {
			return fmt.Errorf("scan appointment crew: %w", err)
		}
		if a := byID[id]; a != nil {
			a.Crew = append(a.Crew, m)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate appointment crew: %w", err)
	}
	return nil
}

// collect scans apptSelect rows, preserving order, and attaches crew.
func collect(ctx context.Context, q Querier, sql string, args ...any) ([]Appointment, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query appointments: %w", err)
	}
	var ptrs []*Appointment
	byID := map[int]*Appointment{}
	for rows.Next() {
		id, a, err := scanAppt(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan appointment: %w", err)
		}
		ptrs = append(ptrs, a)
		byID[id] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate appointments: %w", err)
	}
	if err := loadCrew(ctx, q, byID); err != nil {
		return nil, err
	}
	out := make([]Appointment, len(ptrs))
	for i, p := range ptrs {
		out[i] = *p
	}
	return out, nil
}

// Get loads one appointment with its crew by uuid.
func Get(ctx context.Context, q Querier, uuid string) (*Appointment, error) {
	id, a, err := scanAppt(q.QueryRow(ctx, apptSelect+` WHERE ia.install_appointment_uuid = $1 AND fj.fabrication_job_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get appointment: %w", err)
	}
	if err := loadCrew(ctx, q, map[int]*Appointment{id: a}); err != nil {
		return nil, err
	}
	return a, nil
}

// CanSeeFunc reports whether the caller may see another appointment, querying through q (the write transaction).
type CanSeeFunc func(ctx context.Context, q Querier, apptUUID string) bool

// OwnerAndCrew returns the job owner's users.id and the crew members' users.ids for the IDOR check.
func OwnerAndCrew(ctx context.Context, q Querier, uuid string) (string, []string, error) {
	var apptID int
	var owner string
	err := q.QueryRow(ctx, `
		SELECT ia.install_appointment_id, COALESCE(ou.id::text,'')
		FROM install_appointment ia
		JOIN fabrication_job fj ON fj.fabrication_job_id = ia.fabrication_job_id
		LEFT JOIN employee oe ON oe.employee_id = fj.job_owner_id AND oe.employee_deleted_at IS NULL
		LEFT JOIN users ou ON ou.id = oe.employee_user_id
		WHERE ia.install_appointment_uuid = $1 AND fj.fabrication_job_deleted_at IS NULL`, uuid).Scan(&apptID, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, fmt.Errorf("load appointment owner: %w", err)
	}
	rows, err := q.Query(ctx, `
		SELECT e.employee_user_id::text
		FROM install_appointment_crew iac
		JOIN employee e ON e.employee_id = iac.employee_id
		WHERE iac.install_appointment_id = $1 AND e.employee_user_id IS NOT NULL AND e.employee_deleted_at IS NULL
		ORDER BY e.employee_id`, apptID)
	if err != nil {
		return "", nil, fmt.Errorf("load appointment crew users: %w", err)
	}
	defer rows.Close()
	crew := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return "", nil, fmt.Errorf("scan crew user: %w", err)
		}
		crew = append(crew, u)
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("iterate crew users: %w", err)
	}
	return owner, crew, nil
}

// ListForJob returns every appointment of a job, oldest first.
func ListForJob(ctx context.Context, q Querier, jobUUID string) ([]Appointment, error) {
	return collect(ctx, q, apptSelect+`
		WHERE fj.fabrication_job_uuid = $1 AND fj.fabrication_job_deleted_at IS NULL
		ORDER BY ia.starts_at, ia.install_appointment_id`, jobUUID)
}
