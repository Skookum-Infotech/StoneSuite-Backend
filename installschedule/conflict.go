package installschedule

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// Conflict types and severities reported to clients.
const (
	ConflictCrew = "crew"
	ConflictJob  = "job"
	// SeveritySoft marks an overlap the actor may acknowledge and override.
	SeveritySoft = "soft"

	lockKeyJob = "ia:job:%d"
	lockKeyEmp = "ia:emp:%d"
)

// liveApptPredicate selects appointments that can still conflict: scheduled, or completed but not yet ended.
// $1 is the current instant, supplied from Go.
const liveApptPredicate = `(ia.appointment_status = 'scheduled' OR (ia.appointment_status = 'completed' AND ia.ends_at > $1))`

// Overlaps reports whether two half-open intervals share any instant.
func Overlaps(a, b Interval) bool {
	return a.Start.Before(b.End) && b.Start.Before(a.End)
}

// sortedUniqueIDs returns the ids ascending with duplicates removed.
func sortedUniqueIDs(ids []int) []int {
	out := make([]int, 0, len(ids))
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// lockScope takes transaction-scoped advisory locks on the job, then each employee in ascending order.
func lockScope(ctx context.Context, tx pgx.Tx, jobID int, employeeIDs []int) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, fmt.Sprintf(lockKeyJob, jobID)); err != nil {
		return fmt.Errorf("lock job %d: %w", jobID, err)
	}
	for _, id := range sortedUniqueIDs(employeeIDs) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, fmt.Sprintf(lockKeyEmp, id)); err != nil {
			return fmt.Errorf("lock employee %d: %w", id, err)
		}
	}
	return nil
}

// findConflicts lists crew and job overlaps with iv, excluding excludeApptID (0 for none).
// Conflicts whose appointment canSee rejects, and visible ones beyond ConflictListCap,
// are not detailed; the returned int counts them.
func findConflicts(ctx context.Context, tx pgx.Tx, jobID int, iv Interval, employeeIDs []int, excludeApptID int, canSee func(apptUUID string) bool) ([]Conflict, int, error) {
	now := time.Now()
	lowerBound := iv.Start.Add(-MaxDuration)
	var rows []Conflict

	if ids := sortedUniqueIDs(employeeIDs); len(ids) > 0 {
		crew, err := queryConflicts(ctx, tx, ConflictCrew, `
			SELECT ia.install_appointment_uuid::text, ia.starts_at, ia.ends_at, ia.timezone,
			       iac.employee_id, btrim(e.employee_first_name || ' ' || e.employee_last_name),
			       COALESCE(fj.fabrication_job_number,'')
			FROM install_appointment ia
			JOIN install_appointment_crew iac ON iac.install_appointment_id = ia.install_appointment_id
			JOIN employee e ON e.employee_id = iac.employee_id
			JOIN fabrication_job fj ON fj.fabrication_job_id = ia.fabrication_job_id
			WHERE `+liveApptPredicate+`
			  AND fj.fabrication_job_deleted_at IS NULL
			  AND iac.employee_id = ANY($2)
			  AND ia.install_appointment_id <> $3
			  AND ia.starts_at < $4 AND ia.ends_at > $5 AND ia.starts_at >= $6
			ORDER BY ia.starts_at, ia.install_appointment_id, iac.employee_id`,
			now, ids, excludeApptID, iv.End, iv.Start, lowerBound)
		if err != nil {
			return nil, 0, err
		}
		rows = append(rows, crew...)
	}

	job, err := queryConflicts(ctx, tx, ConflictJob, `
		SELECT ia.install_appointment_uuid::text, ia.starts_at, ia.ends_at, ia.timezone,
		       0, '', COALESCE(fj.fabrication_job_number,'')
		FROM install_appointment ia
		JOIN fabrication_job fj ON fj.fabrication_job_id = ia.fabrication_job_id
		WHERE `+liveApptPredicate+`
		  AND ia.fabrication_job_id = $2
		  AND ia.install_appointment_id <> $3
		  AND ia.starts_at < $4 AND ia.ends_at > $5 AND ia.starts_at >= $6
		ORDER BY ia.starts_at, ia.install_appointment_id`,
		now, jobID, excludeApptID, iv.End, iv.Start, lowerBound)
	if err != nil {
		return nil, 0, err
	}
	rows = append(rows, job...)

	visible := make([]Conflict, 0, len(rows))
	hidden := 0
	for _, c := range rows {
		if canSee == nil || !canSee(c.AppointmentUUID) {
			hidden++
			continue
		}
		if len(visible) >= ConflictListCap {
			hidden++
			continue
		}
		visible = append(visible, c)
	}
	return visible, hidden, nil
}

// queryConflicts runs one conflict query and maps rows to Conflicts of the given type.
func queryConflicts(ctx context.Context, tx pgx.Tx, kind, sql string, args ...any) ([]Conflict, error) {
	rs, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s conflicts: %w", kind, err)
	}
	defer rs.Close()
	var out []Conflict
	for rs.Next() {
		var c Conflict
		var starts, ends time.Time
		var tz string
		if err := rs.Scan(&c.AppointmentUUID, &starts, &ends, &tz, &c.EmployeeID, &c.EmployeeName, &c.JobNumber); err != nil {
			return nil, fmt.Errorf("scan %s conflict: %w", kind, err)
		}
		c.Type, c.Severity = kind, SeveritySoft
		view := Appointment{ID: c.AppointmentUUID, Timezone: tz, StartsAt: starts, EndsAt: ends}
		fillLocal(&view)
		c.StartLocal, c.EndLocal = view.StartLocal, view.EndLocal
		out = append(out, c)
	}
	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s conflicts: %w", kind, err)
	}
	return out, nil
}
