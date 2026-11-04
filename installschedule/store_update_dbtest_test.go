//go:build dbtest

package installschedule

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bookAt creates an appointment for emp on createDay and returns it.
func bookAt(t *testing.T, pool *pgxpool.Pool, jobUUID string, emp, actor int, start, end string) *Appointment {
	t.Helper()
	res, err := Create(context.Background(), pool, chicagoLoc(t), time.Now(), createInput(jobUUID, emp, start, end), actor, allVisible)
	require.NoError(t, err)
	return res.Appointment
}

func strPtr(s string) *string { return &s }

func historyActions(t *testing.T, pool *pgxpool.Pool, uuid string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT h.action FROM install_appointment_history h
		JOIN install_appointment ia ON ia.install_appointment_id = h.install_appointment_id
		WHERE ia.install_appointment_uuid = $1 ORDER BY h.install_appointment_history_id`, uuid)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		require.NoError(t, rows.Scan(&a))
		out = append(out, a)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestUpdate_StaleVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, jobUUID := seedJob(t, pool, owner)
	appt := bookAt(t, pool, jobUUID, emp, owner, "08:00", "12:00")

	in := UpdateInput{Notes: strPtr("first"), ExpectedVersion: appt.Version}
	_, err := Update(ctx, pool, loc, time.Now(), appt.ID, in, owner, allVisible)
	require.NoError(t, err)

	in.Notes = strPtr("second")
	_, err = Update(ctx, pool, loc, time.Now(), appt.ID, in, owner, allVisible)
	require.ErrorIs(t, err, ErrStaleVersion)
	var sv *StaleVersionError
	require.ErrorAs(t, err, &sv)
	assert.Equal(t, appt.Version+1, sv.CurrentVersion)
}

func TestUpdate_RescheduleAndReassign(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, e1 := seedUserEmployee(t, pool, "c1")
	_, _, e2 := seedUserEmployee(t, pool, "c2")
	jobID, jobUUID := seedJob(t, pool, owner)
	appt := bookAt(t, pool, jobUUID, e1, owner, "08:00", "12:00")

	var jobVersion int
	jobVer := func() int {
		require.NoError(t, pool.QueryRow(ctx, `SELECT fabrication_job_record_version FROM fabrication_job WHERE fabrication_job_id=$1`, jobID).Scan(&jobVersion))
		return jobVersion
	}
	jobBefore := jobVer()

	// 10:00-14:00 overlaps the appointment's own old window: it must not conflict with itself.
	in := UpdateInput{
		StartLocal: strPtr(createDay + "T10:00"), EndLocal: strPtr(createDay + "T14:00"),
		Crew:            &[]CrewInput{{EmployeeID: e2, Role: CrewLead}},
		ExpectedVersion: appt.Version, Reason: "customer asked",
	}
	res, err := Update(ctx, pool, loc, time.Now(), appt.ID, in, owner, allVisible)
	require.NoError(t, err)
	assert.Equal(t, []string{ActionReschedule, ActionReassign}, res.Actions)
	assert.Len(t, res.HistoryIDs, 2)
	assert.Equal(t, []int{e2}, res.AddedCrew)
	assert.Equal(t, []int{e1}, res.RemovedCrew)
	assert.Equal(t, appt.Version+1, res.After.Version)
	assert.Equal(t, appt.Version, res.Before.Version)
	assert.Equal(t, createDay+"T10:00", res.After.StartLocal)
	require.Len(t, res.After.Crew, 1)
	assert.Equal(t, e2, res.After.Crew[0].EmployeeID)
	assert.Equal(t, []string{ActionCreate, ActionReschedule, ActionReassign}, historyActions(t, pool, appt.ID))
	assert.Equal(t, jobBefore, jobVer())
}

func TestUpdate_ConflictAckAndRedaction(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, jobA := seedJob(t, pool, owner)
	_, jobB := seedJob(t, pool, owner)
	a := bookAt(t, pool, jobA, emp, owner, "08:00", "10:00")
	b := bookAt(t, pool, jobB, emp, owner, "12:00", "14:00")

	in := UpdateInput{StartLocal: strPtr(createDay + "T09:00"), EndLocal: strPtr(createDay + "T13:00"), ExpectedVersion: b.Version}
	_, err := Update(ctx, pool, loc, time.Now(), b.ID, in, owner, func(string) bool { return false })
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Len(t, ce.Conflicts, 1)
	assert.Empty(t, ce.Conflicts[0].AppointmentUUID)
	assert.Empty(t, ce.Conflicts[0].JobNumber)

	in.AcknowledgeConflicts = true
	res, err := Update(ctx, pool, loc, time.Now(), b.ID, in, owner, func(string) bool { return false })
	require.NoError(t, err)
	var acked string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT conflicts_acknowledged::text FROM install_appointment_history WHERE install_appointment_history_id=$1`,
		res.HistoryIDs[0]).Scan(&acked))
	assert.Contains(t, acked, a.ID)
}

func TestUpdate_NoChangeIsValidationError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, jobUUID := seedJob(t, pool, owner)
	appt := bookAt(t, pool, jobUUID, emp, owner, "08:00", "12:00")

	var ve *ValidationError
	_, err := Update(ctx, pool, loc, time.Now(), appt.ID, UpdateInput{ExpectedVersion: appt.Version}, owner, allVisible)
	require.ErrorAs(t, err, &ve)

	// Supplying the current values is also "no change".
	same := UpdateInput{StartLocal: &appt.StartLocal, EndLocal: &appt.EndLocal, ExpectedVersion: appt.Version}
	_, err = Update(ctx, pool, loc, time.Now(), appt.ID, same, owner, allVisible)
	require.ErrorAs(t, err, &ve)

	got, err := Get(ctx, pool, appt.ID)
	require.NoError(t, err)
	assert.Equal(t, appt.Version, got.Version)
}

func TestUpdate_NotesVisitTypeAndStatusGuards(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, jobUUID := seedJob(t, pool, owner)
	appt := bookAt(t, pool, jobUUID, emp, owner, "08:00", "12:00")

	vt := VisitService
	res, err := Update(ctx, pool, loc, time.Now(), appt.ID,
		UpdateInput{Notes: strPtr("bring ladder"), VisitType: &vt, ExpectedVersion: appt.Version}, owner, allVisible)
	require.NoError(t, err)
	assert.Equal(t, []string{ActionUpdate}, res.Actions)
	assert.Equal(t, "bring ladder", res.After.Notes)
	assert.Equal(t, VisitService, res.After.VisitType)

	_, err = Update(ctx, pool, loc, time.Now(), appt.ID,
		UpdateInput{Notes: strPtr(strings.Repeat("x", MaxNotes+1)), ExpectedVersion: res.After.Version}, owner, allVisible)
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)

	_, err = Cancel(ctx, pool, appt.ID, res.After.Version, "no longer needed", owner)
	require.NoError(t, err)
	_, err = Update(ctx, pool, loc, time.Now(), appt.ID,
		UpdateInput{Notes: strPtr("late"), ExpectedVersion: res.After.Version + 1}, owner, allVisible)
	assert.ErrorIs(t, err, ErrInvalidStatus)

	_, err = Update(ctx, pool, loc, time.Now(), "00000000-0000-0000-0000-000000000000",
		UpdateInput{Notes: strPtr("x"), ExpectedVersion: 1}, owner, allVisible)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestUpdate_RescheduleClosedJob(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	jobID, jobUUID := seedJob(t, pool, owner)
	appt := bookAt(t, pool, jobUUID, emp, owner, "08:00", "12:00")
	setJobStatus(t, pool, jobID, "CANC")

	in := UpdateInput{StartLocal: strPtr(createDay + "T09:00"), EndLocal: strPtr(createDay + "T13:00"), ExpectedVersion: appt.Version}
	_, err := Update(ctx, pool, loc, time.Now(), appt.ID, in, owner, allVisible)
	assert.ErrorIs(t, err, ErrJobClosed)
}
func TestUpdateAndCreate_ConcurrentNoDeadlock(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	emps := make([]int, 3)
	for i := range emps {
		_, _, emps[i] = seedUserEmployee(t, pool, "crew")
	}
	_, jobUUID := seedJob(t, pool, owner)
	appt := bookAt(t, pool, jobUUID, emps[0], owner, "06:00", "07:00")

	const workers = 10
	jobs := make([]string, workers)
	for i := range jobs {
		_, jobs[i] = seedJob(t, pool, owner)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				crew := []CrewInput{{EmployeeID: emps[(i+1)%3], Role: CrewLead}, {EmployeeID: emps[(i+2)%3], Role: CrewMemberRole}}
				v := currentVersion(t, pool, appt.ID)
				_, err = Update(ctx, pool, loc, time.Now(), appt.ID,
					UpdateInput{Crew: &crew, AcknowledgeConflicts: true, ExpectedVersion: v}, owner, allVisible)
			} else {
				in := createInput(jobs[i], emps[i%3], "08:00", "12:00")
				in.Crew = append(in.Crew, CrewInput{EmployeeID: emps[(i+1)%3], Role: CrewMemberRole})
				in.AcknowledgeConflicts = true
				_, err = Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
			}
			// Stale versions are expected under contention; anything else (deadlock, SQL error) is not.
			var sv *StaleVersionError
			var ve *ValidationError
			if err != nil && !errors.As(err, &sv) && !errors.As(err, &ve) {
				t.Errorf("worker %d: %v", i, err)
			}
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent update/create did not finish: probable deadlock")
	}
}

func currentVersion(t *testing.T, pool *pgxpool.Pool, uuid string) int {
	t.Helper()
	var v int
	if err := pool.QueryRow(context.Background(),
		`SELECT record_version FROM install_appointment WHERE install_appointment_uuid=$1`, uuid).Scan(&v); err != nil {
		t.Errorf("read version: %v", err)
	}
	return v
}
