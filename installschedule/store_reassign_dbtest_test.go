//go:build dbtest

package installschedule

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdate_AddCrewIgnoresPreexistingJobOverlap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, e1 := seedUserEmployee(t, pool, "c1")
	_, _, e2 := seedUserEmployee(t, pool, "c2")
	_, _, e3 := seedUserEmployee(t, pool, "c3")
	_, jobUUID := seedJob(t, pool, owner)
	bookAt(t, pool, jobUUID, e1, owner, "08:00", "12:00")

	in := createInput(jobUUID, e2, "09:00", "11:00")
	in.AcknowledgeConflicts = true
	second, err := Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
	require.NoError(t, err)

	crew := []CrewInput{{EmployeeID: e2, Role: CrewLead}, {EmployeeID: e3, Role: CrewMemberRole}}
	res, err := Update(ctx, pool, loc, time.Now(), second.Appointment.ID,
		UpdateInput{Crew: &crew, ExpectedVersion: second.Appointment.Version}, owner, allVisible)
	require.NoError(t, err)
	assert.Equal(t, []int{e3}, res.AddedCrew)
}

func TestUpdate_LeadSwap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, a := seedUserEmployee(t, pool, "a")
	_, _, b := seedUserEmployee(t, pool, "b")
	_, jobUUID := seedJob(t, pool, owner)
	in := createInput(jobUUID, a, "08:00", "12:00")
	in.Crew = append(in.Crew, CrewInput{EmployeeID: b, Role: CrewMemberRole})
	created, err := Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
	require.NoError(t, err)

	crew := []CrewInput{{EmployeeID: a, Role: CrewMemberRole}, {EmployeeID: b, Role: CrewLead}}
	res, err := Update(ctx, pool, loc, time.Now(), created.Appointment.ID,
		UpdateInput{Crew: &crew, ExpectedVersion: created.Appointment.Version}, owner, allVisible)
	require.NoError(t, err)
	assert.Equal(t, []string{ActionReassign}, res.Actions)
	assert.Empty(t, res.AddedCrew)
	assert.Empty(t, res.RemovedCrew)
	roles := map[int]CrewRole{}
	for _, m := range res.After.Crew {
		roles[m.EmployeeID] = m.Role
	}
	assert.Equal(t, map[int]CrewRole{a: CrewMemberRole, b: CrewLead}, roles)
}

func TestUpdate_AddCrewWithRealConflict(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, e1 := seedUserEmployee(t, pool, "c1")
	_, _, busy := seedUserEmployee(t, pool, "busy")
	_, jobA := seedJob(t, pool, owner)
	_, jobB := seedJob(t, pool, owner)
	target := bookAt(t, pool, jobA, e1, owner, "08:00", "12:00")
	other := bookAt(t, pool, jobB, busy, owner, "10:00", "14:00")

	crew := []CrewInput{{EmployeeID: e1, Role: CrewLead}, {EmployeeID: busy, Role: CrewMemberRole}}
	in := UpdateInput{Crew: &crew, ExpectedVersion: target.Version}
	_, err := Update(ctx, pool, loc, time.Now(), target.ID, in, owner, allVisible)
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Len(t, ce.Conflicts, 1)
	assert.Equal(t, ConflictCrew, ce.Conflicts[0].Type)
	assert.Equal(t, busy, ce.Conflicts[0].EmployeeID)
	assert.Equal(t, other.ID, ce.Conflicts[0].AppointmentUUID)

	in.AcknowledgeConflicts = true
	res, err := Update(ctx, pool, loc, time.Now(), target.ID, in, owner, allVisible)
	require.NoError(t, err)
	assert.Equal(t, []int{busy}, res.AddedCrew)
}
