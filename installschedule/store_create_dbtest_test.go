//go:build dbtest

package installschedule

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	createDay         = "2032-06-10"
	concurrentWriters = 20
)

func allVisible(string) bool { return true }

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

	first, err := Create(ctx, pool, loc, createInput(jobUUID, emp, "08:00", "12:00"), owner, allVisible)
	require.NoError(t, err)

	in := createInput(jobUUID2, emp, "10:00", "14:00")
	_, err = Create(ctx, pool, loc, in, owner, allVisible)
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Len(t, ce.Conflicts, 1)
	assert.Equal(t, "crew", ce.Conflicts[0].Type)
	assert.Equal(t, "soft", ce.Conflicts[0].Severity)
	assert.Equal(t, emp, ce.Conflicts[0].EmployeeID)
	assert.Equal(t, first.Appointment.ID, ce.Conflicts[0].AppointmentUUID)

	// Hidden conflicts are counted but not detailed.
	_, err = Create(ctx, pool, loc, in, owner, func(string) bool { return false })
	require.ErrorAs(t, err, &ce)
	assert.Empty(t, ce.Conflicts)
	assert.Equal(t, 1, ce.More)

	in.AcknowledgeConflicts = true
	second, err := Create(ctx, pool, loc, in, owner, allVisible)
	require.NoError(t, err)
	assert.False(t, second.Replayed)

	var acked string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT conflicts_acknowledged::text FROM install_appointment_history WHERE install_appointment_history_id=$1`,
		second.HistoryID).Scan(&acked))
	assert.Contains(t, acked, first.Appointment.ID)
}

func TestCreate_JobConflict(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, e1 := seedUserEmployee(t, pool, "c1")
	_, _, e2 := seedUserEmployee(t, pool, "c2")
	_, jobUUID := seedJob(t, pool, owner)

	_, err := Create(ctx, pool, loc, createInput(jobUUID, e1, "08:00", "12:00"), owner, allVisible)
	require.NoError(t, err)
	_, err = Create(ctx, pool, loc, createInput(jobUUID, e2, "09:00", "10:00"), owner, allVisible)
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
	a, err := Create(ctx, pool, loc, in, owner, allVisible)
	require.NoError(t, err)
	assert.False(t, a.Replayed)
	b, err := Create(ctx, pool, loc, in, owner, allVisible)
	require.NoError(t, err)
	assert.True(t, b.Replayed)
	assert.Equal(t, a.Appointment.ID, b.Appointment.ID)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM install_appointment WHERE fabrication_job_id=$1`, jobID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_ConcurrentSameCrew(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	jobs := make([]string, concurrentWriters)
	for i := range jobs {
		_, jobs[i] = seedJob(t, pool, owner)
	}

	var wg sync.WaitGroup
	var ok, conflicts, other atomic.Int32
	for i := 0; i < concurrentWriters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := Create(ctx, pool, loc, createInput(jobs[i], emp, "08:00", "12:00"), owner, allVisible)
			var ce *ConflictError
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &ce):
				conflicts.Add(1)
			default:
				other.Add(1)
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	assert.EqualValues(t, 1, ok.Load())
	assert.EqualValues(t, concurrentWriters-1, conflicts.Load())
	assert.EqualValues(t, 0, other.Load())
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
		_, err := Create(ctx, pool, loc, createInput(jobUUID, emp, "08:00", "12:00"), owner, allVisible)
		assert.ErrorIs(t, err, ErrJobClosed, code)
	}
}

func TestCreate_UnknownJobAndBadCrew(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, jobUUID := seedJob(t, pool, owner)

	_, err := Create(ctx, pool, loc, createInput("00000000-0000-0000-0000-000000000000", owner, "08:00", "12:00"), owner, allVisible)
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = Create(ctx, pool, loc, createInput(jobUUID, 999999999, "08:00", "12:00"), owner, allVisible)
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
	_, err := Create(ctx, pool, loc, createInput(jobUUID, emp, "08:00", "12:00"), owner, allVisible)
	require.NoError(t, err)
	assert.Equal(t, before, version())
}
