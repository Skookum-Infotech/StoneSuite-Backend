//go:build dbtest

package installschedule

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventTypes returns the event types stored for an appointment, oldest first.
func eventTypes(t *testing.T, pool *pgxpool.Pool, apptUUID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT event_type FROM domain_event WHERE resource = $1 AND record_id = $2 ORDER BY id`, ResourceInstallAppointment, apptUUID)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestCreate_EmitsDomainEvent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, identity, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, jobUUID := seedJob(t, pool, owner)

	in := createInput(jobUUID, emp, "08:00", "12:00")
	in.Notes = "secret gate code"
	in.ActorUserID = identity
	res, err := Create(ctx, pool, chicagoLoc(t), time.Now(), in, owner, allVisible)
	require.NoError(t, err)
	assert.Equal(t, []string{EventScheduled}, eventTypes(t, pool, res.Appointment.ID))

	var payload string
	var actor string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT payload::text, actor_user_id::text FROM domain_event WHERE record_id = $1`, res.Appointment.ID).Scan(&payload, &actor))
	assert.Equal(t, identity, actor)
	assert.NotContains(t, payload, "secret gate code")
	var p eventPayload
	require.NoError(t, json.Unmarshal([]byte(payload), &p))
	assert.Equal(t, res.Appointment.FabricationJobID, p.FabricationJobID)
	assert.Equal(t, res.Appointment.Version, p.Version)
}

func TestCreate_EventFailureRollsBackWrite(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	jobID, jobUUID := seedJob(t, pool, owner)

	in := createInput(jobUUID, emp, "08:00", "12:00")
	in.ActorUserID = "not-a-uuid" // makes the in-transaction Emit fail after the insert
	_, err := Create(ctx, pool, chicagoLoc(t), time.Now(), in, owner, allVisible)
	require.Error(t, err)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM install_appointment WHERE fabrication_job_id = $1`, jobID).Scan(&n))
	assert.Zero(t, n)
}

func TestUpdateCancelComplete_EmitEvents(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, identity, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, _, emp2 := seedUserEmployee(t, pool, "crew2")
	_, jobUUID := seedJob(t, pool, owner)
	appt := bookAt(t, pool, jobUUID, emp, owner, "08:00", "12:00")

	notes := "moved the notes"
	crew := []CrewInput{{EmployeeID: emp2, Role: CrewLead}}
	res, err := Update(ctx, pool, chicagoLoc(t), time.Now(), appt.ID,
		UpdateInput{Notes: &notes, Crew: &crew, ExpectedVersion: appt.Version, ActorUserID: identity}, owner, allVisible)
	require.NoError(t, err)
	assert.Equal(t, []string{EventScheduled, EventReassigned, EventUpdated}, eventTypes(t, pool, appt.ID))

	_, err = Cancel(ctx, pool, appt.ID, res.After.Version, "weather", owner, identity)
	require.NoError(t, err)
	assert.Equal(t, []string{EventScheduled, EventReassigned, EventUpdated, EventCancelled}, eventTypes(t, pool, appt.ID))

	appt2 := bookAt(t, pool, jobUUID, emp, owner, "13:00", "14:00")
	_, err = Complete(ctx, pool, appt2.ID, appt2.Version, owner, appt2.StartsAt, identity)
	require.NoError(t, err)
	assert.Equal(t, []string{EventScheduled, EventCompleted}, eventTypes(t, pool, appt2.ID))
}
