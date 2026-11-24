//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// instJob inserts a fabrication job (with its sales order) for customer cust in the given job status.
func (f *arFixture) instJob(cust int, jobStatus string, deleted bool) int {
	var soID, jobID int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id)
		VALUES ($1, $2, $3) RETURNING sales_order_id`,
		f.typeID("SORD"), f.statusID("SORD", "OPEN"), cust).Scan(&soID))
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO fabrication_job (fabrication_job_number, record_type, fabrication_job_status, sales_order_id, fabrication_job_customer_id)
		VALUES ('FJOB-T' || ($4::int)::text, $1, $2, $3, $5) RETURNING fabrication_job_id`,
		f.typeID("FJOB"), f.statusID("FJOB", jobStatus), soID, soID, cust).Scan(&jobID))
	if deleted {
		_, err := f.tx.Exec(f.ctx, `UPDATE fabrication_job SET fabrication_job_deleted_at = now(), fabrication_job_deleted_by = 1 WHERE fabrication_job_id = $1`, jobID)
		require.NoError(f.t, err)
	}
	return jobID
}

type instAppt struct {
	job       int
	visitType string
	status    string // scheduled | cancelled | completed
	day       *time.Time
	crew      []int
}

// instAppointment inserts an appointment starting 14:00 UTC on day (America/Chicago) and returns its uuid.
func (f *arFixture) instAppointment(a instAppt) string {
	if a.visitType == "" {
		a.visitType = "install"
	}
	start := time.Date(a.day.Year(), a.day.Month(), a.day.Day(), 14, 0, 0, 0, time.UTC)
	var id int
	var uuid string
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO install_appointment (fabrication_job_id, visit_type, appointment_status, starts_at, ends_at,
			local_start_date, local_end_date, timezone, cancelled_at, completed_at, created_by, updated_by)
		VALUES ($1, $2, $3::text, $4, $5, $6::date, $6::date, 'America/Chicago',
			CASE WHEN $3::text = 'cancelled' THEN now() END, CASE WHEN $3::text = 'completed' THEN now() END, 1, 1)
		RETURNING install_appointment_id, install_appointment_uuid::text`,
		a.job, a.visitType, a.status, start, start.Add(2*time.Hour), *a.day).Scan(&id, &uuid))
	for i, emp := range a.crew {
		role := "member"
		if i == 0 {
			role = "lead"
		}
		_, err := f.tx.Exec(f.ctx, `INSERT INTO install_appointment_crew (install_appointment_id, employee_id, crew_role, assigned_by) VALUES ($1, $2, $3, 1)`, id, emp, role)
		require.NoError(f.t, err)
	}
	return uuid
}

func TestInstallListCandidates(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	jobOK := f.instJob(cust, "INST", false)
	crewA, _ := f.employeeWithUser("crew-a@x.io", "active")
	crewB := f.employeeNoUser("crew-b@x.io")

	tomorrow := f.instAppointment(instAppt{job: jobOK, status: "scheduled", day: dayOffset(1), crew: []int{crewA, crewB}})
	today := f.instAppointment(instAppt{job: jobOK, status: "scheduled", day: dayOffset(0), crew: []int{crewA}, visitType: "service"})
	f.instAppointment(instAppt{job: jobOK, status: "cancelled", day: dayOffset(1), crew: []int{crewA}})
	f.instAppointment(instAppt{job: jobOK, status: "completed", day: dayOffset(1), crew: []int{crewA}})
	f.instAppointment(instAppt{job: jobOK, status: "scheduled", day: dayOffset(5), crew: []int{crewA}})
	f.instAppointment(instAppt{job: jobOK, status: "scheduled", day: dayOffset(-1), crew: []int{crewA}})
	f.instAppointment(instAppt{job: f.instJob(cust, "CANC", false), status: "scheduled", day: dayOffset(1), crew: []int{crewA}})
	f.instAppointment(instAppt{job: f.instJob(cust, "INST", true), status: "scheduled", day: dayOffset(1), crew: []int{crewA}})
	f.instAppointment(instAppt{job: jobOK, status: "scheduled", day: dayOffset(1)}) // nobody on the crew

	rows, err := listInstallCandidates(f.ctx, f.tx, arToday)
	require.NoError(t, err)
	type key struct {
		uuid string
		emp  int
	}
	got := map[key]InstallRow{}
	for _, r := range rows {
		got[key{r.UUID, r.EmployeeID}] = r
	}
	require.Len(t, got, 3, "tomorrow x two crew, today x one crew; nothing cancelled, completed, out of window, on a cancelled or deleted job, or with no crew")
	assert.Equal(t, "crew-a@x.io", got[key{tomorrow, crewA}].Email)
	assert.Empty(t, got[key{tomorrow, crewB}].Email, "a crew member with no active user has no recipient")
	assert.Equal(t, "service", got[key{today, crewA}].VisitType)
	assert.Equal(t, "Acme", got[key{tomorrow, crewA}].CustomerName)
	assert.Equal(t, "America/Chicago", got[key{tomorrow, crewA}].Timezone)
	assert.True(t, sameDay(got[key{tomorrow, crewA}].LocalStart, *dayOffset(1)))
}

func TestInstallEligible(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	crewA, _ := f.employeeWithUser("elig-a@x.io", "active")
	crewB, _ := f.employeeWithUser("elig-b@x.io", "active")
	planned := *dayOffset(1)
	mk := func(job int, status string, day *time.Time, crew ...int) string {
		return f.instAppointment(instAppt{job: job, status: status, day: day, crew: crew})
	}
	job := f.instJob(cust, "INST", false)

	good := mk(job, "scheduled", dayOffset(1), crewA)
	ok, reason, live, err := installEligible(f.ctx, f.tx, good, "ELIG-A@x.io", planned)
	require.NoError(t, err)
	assert.True(t, ok, reason)
	assert.Equal(t, "elig-a@x.io", live.Email, "the match is case-insensitive")

	tests := []struct {
		name  string
		uuid  string
		email string
		want  string
	}{
		{"recipient left the crew", mk(job, "scheduled", dayOffset(1), crewA), "elig-b@x.io", reasonCrewChanged},
		{"cancelled", mk(job, "cancelled", dayOffset(1), crewA), "elig-a@x.io", reasonCancelled},
		{"completed", mk(job, "completed", dayOffset(1), crewA), "elig-a@x.io", reasonCompleted},
		{"rescheduled", mk(job, "scheduled", dayOffset(4), crewA), "elig-a@x.io", reasonAnchorChanged},
		{"job cancelled", mk(f.instJob(cust, "CANC", false), "scheduled", dayOffset(1), crewA), "elig-a@x.io", reasonCancelled},
		{"job deleted", mk(f.instJob(cust, "INST", true), "scheduled", dayOffset(1), crewA), "elig-a@x.io", reasonVoided},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason, _, err := installEligible(f.ctx, f.tx, tc.uuid, tc.email, planned)
			require.NoError(t, err)
			assert.False(t, ok)
			assert.Equal(t, tc.want, reason)
		})
	}

	// A crew member added later is eligible, and the original still is.
	two := mk(job, "scheduled", dayOffset(1), crewA, crewB)
	for _, email := range []string{"elig-a@x.io", "elig-b@x.io"} {
		ok, reason, _, err := installEligible(f.ctx, f.tx, two, email, planned)
		require.NoError(t, err)
		assert.True(t, ok, "%s: %s", email, reason)
	}

	ok, reason, _, err = installEligible(f.ctx, f.tx, "123e4567-e89b-12d3-a456-426614174999", "elig-a@x.io", planned)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, reasonVoided, reason)
}
