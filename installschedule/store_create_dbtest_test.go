//go:build dbtest

package installschedule

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	createDay         = "2032-06-10"
	concurrentWriters = 20
)

func allVisible(context.Context, Querier, string) bool { return true }

func createInput(jobUUID string, empID int, start, end string) CreateInput {
	return CreateInput{
		FabricationJobUUID: jobUUID, VisitType: VisitInstall,
		StartLocal: createDay + "T" + start, EndLocal: createDay + "T" + end,
		Crew: []CrewInput{{EmployeeID: empID, Role: CrewLead}},
	}
}

func setJobStatus(t *testing.T, pool *pgxpool.Pool, jobID int, code string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		UPDATE fabrication_job SET fabrication_job_status = (
			SELECT s.record_status_id FROM lkp_record_status s
			JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type
			WHERE t.record_type_code = 'FJOB' AND s.record_status_code = $2)
		WHERE fabrication_job_id = $1`, jobID, code)
	require.NoError(t, err)
}

func TestCreate_SoftConflictThenAck(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, jobUUID := seedJob(t, pool, owner)
	_, jobUUID2 := seedJob(t, pool, owner)

	first, err := Create(ctx, pool, loc, time.Now(), createInput(jobUUID, emp, "08:00", "12:00"), owner, allVisible)
	require.NoError(t, err)

	in := createInput(jobUUID2, emp, "10:00", "14:00")
	_, err = Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Len(t, ce.Conflicts, 1)
	assert.Equal(t, "crew", ce.Conflicts[0].Type)
	assert.Equal(t, "soft", ce.Conflicts[0].Severity)
	assert.Equal(t, emp, ce.Conflicts[0].EmployeeID)
	assert.Equal(t, first.Appointment.ID, ce.Conflicts[0].AppointmentUUID)

	// Hidden conflicts are redacted to type, employee and times.
	_, err = Create(ctx, pool, loc, time.Now(), in, owner, func(context.Context, Querier, string) bool { return false })
	require.ErrorAs(t, err, &ce)
	require.Len(t, ce.Conflicts, 1)
	assert.Equal(t, ConflictCrew, ce.Conflicts[0].Type)
	assert.Equal(t, emp, ce.Conflicts[0].EmployeeID)
	assert.NotEmpty(t, ce.Conflicts[0].EmployeeName)
	assert.NotEmpty(t, ce.Conflicts[0].StartLocal)
	assert.Empty(t, ce.Conflicts[0].AppointmentUUID)
	assert.Empty(t, ce.Conflicts[0].JobNumber)
	assert.Zero(t, ce.More)

	in.AcknowledgeConflicts = true
	second, err := Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
	require.NoError(t, err)
	assert.False(t, second.Replayed)

	var acked string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT conflicts_acknowledged::text FROM install_appointment_history WHERE install_appointment_history_id=$1`,
		second.HistoryID).Scan(&acked))
	assert.Contains(t, acked, first.Appointment.ID)

	// Acknowledging with a viewer who sees nothing still records the full, unredacted conflict.
	in.StartLocal, in.EndLocal = createDay+"T09:00", createDay+"T11:00"
	_, jobUUID3 := seedJob(t, pool, owner)
	in.FabricationJobUUID = jobUUID3
	third, err := Create(ctx, pool, loc, time.Now(), in, owner, func(context.Context, Querier, string) bool { return false })
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT conflicts_acknowledged::text FROM install_appointment_history WHERE install_appointment_history_id=$1`,
		third.HistoryID).Scan(&acked))
	assert.Contains(t, acked, first.Appointment.ID)
	assert.Contains(t, acked, second.Appointment.ID)
}

func TestCreate_JobConflict(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, e1 := seedUserEmployee(t, pool, "c1")
	_, _, e2 := seedUserEmployee(t, pool, "c2")
	_, jobUUID := seedJob(t, pool, owner)

	_, err := Create(ctx, pool, loc, time.Now(), createInput(jobUUID, e1, "08:00", "12:00"), owner, allVisible)
	require.NoError(t, err)
	_, err = Create(ctx, pool, loc, time.Now(), createInput(jobUUID, e2, "09:00", "10:00"), owner, allVisible)
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Len(t, ce.Conflicts, 1)
	assert.Equal(t, "job", ce.Conflicts[0].Type)
}

func TestCreate_IdempotentRequestID(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	jobID, jobUUID := seedJob(t, pool, owner)

	in := createInput(jobUUID, emp, "08:00", "12:00")
	in.RequestID = "6f1c1f0e-2d0e-4f55-9a53-0f5d2b1a7c11"
	a, err := Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
	require.NoError(t, err)
	assert.False(t, a.Replayed)
	b, err := Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
	require.NoError(t, err)
	assert.True(t, b.Replayed)
	assert.Equal(t, a.Appointment.ID, b.Appointment.ID)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM install_appointment WHERE fabrication_job_id=$1`, jobID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_ClosedJob(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	for _, code := range []string{"CANC", "COMP"} {
		jobID, jobUUID := seedJob(t, pool, owner)
		setJobStatus(t, pool, jobID, code)
		_, err := Create(ctx, pool, loc, time.Now(), createInput(jobUUID, emp, "08:00", "12:00"), owner, allVisible)
		assert.ErrorIs(t, err, ErrJobClosed, code)
	}
}

func TestCreate_UnknownJobAndBadCrew(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, jobUUID := seedJob(t, pool, owner)

	_, err := Create(ctx, pool, loc, time.Now(), createInput("00000000-0000-0000-0000-000000000000", owner, "08:00", "12:00"), owner, allVisible)
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = Create(ctx, pool, loc, time.Now(), createInput(jobUUID, 999999999, "08:00", "12:00"), owner, allVisible)
	var ve *ValidationError
	assert.ErrorAs(t, err, &ve)
}

func TestCreate_DoesNotTouchJobVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	jobID, jobUUID := seedJob(t, pool, owner)
	version := func() int {
		var v int
		require.NoError(t, pool.QueryRow(ctx, `SELECT fabrication_job_record_version FROM fabrication_job WHERE fabrication_job_id=$1`, jobID).Scan(&v))
		return v
	}
	before := version()
	_, err := Create(ctx, pool, loc, time.Now(), createInput(jobUUID, emp, "08:00", "12:00"), owner, allVisible)
	require.NoError(t, err)
	assert.Equal(t, before, version())
}

func TestCreate_AllDayFourteenDaysAcrossFallBack(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, jobUUID := seedJob(t, pool, owner)
	in := CreateInput{
		FabricationJobUUID: jobUUID, VisitType: VisitInstall, AllDay: true,
		StartLocal: "2026-10-31", EndLocal: "2026-11-13",
		Crew: []CrewInput{{EmployeeID: emp, Role: CrewLead}},
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, loc)
	res, err := Create(ctx, pool, loc, now, in, owner, allVisible)
	require.NoError(t, err)
	assert.True(t, res.Appointment.AllDay)
}

func TestCreate_CompletedPastDoesNotConflict(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	jobID, _ := seedJob(t, pool, owner)
	_, jobUUID2 := seedJob(t, pool, owner)
	start := time.Date(2032, 6, 10, 8, 0, 0, 0, loc)
	done := seedAppt(t, pool, jobID, start, start.Add(4*time.Hour), false, emp)
	_, err := pool.Exec(ctx, `UPDATE install_appointment SET appointment_status='completed', completed_at=$2, completed_by=1
		WHERE install_appointment_uuid=$1`, done, start)
	require.NoError(t, err)

	in := createInput(jobUUID2, emp, "10:00", "11:00")
	// Still before the completed visit's end: it counts.
	_, err = Create(ctx, pool, loc, start.Add(time.Hour), in, owner, allVisible)
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	// After its end: it no longer conflicts.
	_, err = Create(ctx, pool, loc, start.Add(26*time.Hour), in, owner, allVisible)
	require.NoError(t, err)
}
