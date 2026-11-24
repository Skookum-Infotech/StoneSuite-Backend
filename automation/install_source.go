package automation

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// resourceInstallAppointment is automation_deliveries.resource for install reminders.
	resourceInstallAppointment = "install_appointment"
	// installStatusScheduled is install_appointment.appointment_status of a live visit.
	installStatusScheduled = "scheduled"
	installStatusCancelled = "cancelled"
	installStatusCompleted = "completed"
	// jobStatusCancelled is the lkp_record_status code of a cancelled fabrication job.
	jobStatusCancelled = "CANC"
	// installLookbackDays / installLookaheadDays bound the candidate scan around local_start_date;
	// a visit that already started is never reminded about.
	installLookbackDays  = 0
	installLookaheadDays = 2

	reasonCancelled = "cancelled"
	reasonCompleted = "completed"
	// reasonCrewChanged drops a delivery whose recipient is no longer on the visit's crew.
	reasonCrewChanged = "crew_changed"
)

// InstallRow is one crew member of one site visit. The user fields are empty
// unless the employee resolves to an active user.
type InstallRow struct {
	UUID         string
	JobNumber    string
	CustomerName string
	VisitType    string
	StartsAt     time.Time
	AllDay       bool
	Timezone     string
	LocalStart   time.Time
	Status       string
	JobDeleted   bool
	JobCancelled bool
	EmployeeID   int
	UserID       string
	IdentityID   string
	Email        string
	Name         string
}

const installSelect = `
SELECT a.install_appointment_uuid::text, COALESCE(j.fabrication_job_number, ''), c.customer_name,
       a.visit_type, a.starts_at, a.all_day, a.timezone, a.local_start_date, a.appointment_status,
       (j.fabrication_job_deleted_at IS NOT NULL), (js.record_status_code = $1),
       cr.employee_id, COALESCE(u.id::text, ''), COALESCE(u.identity_id::text, ''),
       COALESCE(u.email, ''), COALESCE(u.full_name, '')
FROM install_appointment a
JOIN fabrication_job j ON j.fabrication_job_id = a.fabrication_job_id
JOIN lkp_record_status js ON js.record_status_id = j.fabrication_job_status
JOIN customer c ON c.customer_id = j.fabrication_job_customer_id
JOIN install_appointment_crew cr ON cr.install_appointment_id = a.install_appointment_id
JOIN employee e ON e.employee_id = cr.employee_id AND e.employee_deleted_at IS NULL
LEFT JOIN users u ON u.id = e.employee_user_id AND u.status = '` + userStatusActive + `'
`

// scanInstallRows runs a query built on installSelect and decodes every row.
func scanInstallRows(ctx context.Context, q Querier, sql string, args ...any) ([]InstallRow, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query install rows: %w", err)
	}
	defer rows.Close()
	var out []InstallRow
	for rows.Next() {
		var r InstallRow
		if err := rows.Scan(&r.UUID, &r.JobNumber, &r.CustomerName, &r.VisitType, &r.StartsAt, &r.AllDay,
			&r.Timezone, &r.LocalStart, &r.Status, &r.JobDeleted, &r.JobCancelled,
			&r.EmployeeID, &r.UserID, &r.IdentityID, &r.Email, &r.Name); err != nil {
			return nil, fmt.Errorf("scan install row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate install rows: %w", err)
	}
	return out, nil
}

// listInstallCandidates returns one row per crew member of every scheduled,
// live visit whose local start date is within [today, today+2d].
func listInstallCandidates(ctx context.Context, q Querier, today time.Time) ([]InstallRow, error) {
	return scanInstallRows(ctx, q, installSelect+`
WHERE a.appointment_status = '`+installStatusScheduled+`'
  AND j.fabrication_job_deleted_at IS NULL
  AND js.record_status_code <> $1
  AND a.local_start_date BETWEEN $2::date - $3::int AND $2::date + $4::int
ORDER BY a.local_start_date, a.install_appointment_id, cr.employee_id`,
		jobStatusCancelled, today, installLookbackDays, installLookaheadDays)
}

// installEligible re-checks one visit for one recipient live at send time.
// reason "" means eligible; the returned row is the recipient's crew row.
func installEligible(ctx context.Context, q Querier, uuid, recipientEmail string, plannedStart time.Time) (ok bool, reason string, live InstallRow, err error) {
	rows, err := scanInstallRows(ctx, q, installSelect+` WHERE a.install_appointment_uuid = $2::uuid`, jobStatusCancelled, uuid)
	if err != nil {
		return false, "", InstallRow{}, fmt.Errorf("install eligibility %s: %w", uuid, err)
	}
	if len(rows) == 0 {
		return false, reasonVoided, InstallRow{}, nil
	}
	for _, r := range rows {
		if equalEmail(r.Email, recipientEmail) {
			live = r
			reason = installReason(r, plannedStart)
			return reason == "", reason, live, nil
		}
	}
	// The visit exists but the recipient is no longer on its crew (or lost their account).
	if reason = installReason(rows[0], plannedStart); reason != "" && reason != reasonNoEmail {
		return false, reason, InstallRow{}, nil
	}
	return false, reasonCrewChanged, InstallRow{}, nil
}

// installReason maps live facts to the first matching skip reason ("" = eligible).
func installReason(r InstallRow, planned time.Time) string {
	switch {
	case r.JobDeleted:
		return reasonVoided
	case r.Status == installStatusCancelled || r.JobCancelled:
		return reasonCancelled
	case r.Status == installStatusCompleted:
		return reasonCompleted
	case !sameDay(r.LocalStart, planned):
		return reasonAnchorChanged
	case r.Email == "":
		return reasonNoEmail
	}
	return ""
}

// equalEmail compares addresses case-insensitively.
func equalEmail(a, b string) bool { return strings.EqualFold(a, b) }
