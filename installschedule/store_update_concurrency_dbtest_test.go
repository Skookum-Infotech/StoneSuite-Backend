//go:build dbtest

package installschedule

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func currentVersion(t *testing.T, pool *pgxpool.Pool, uuid string) int {
	t.Helper()
	var v int
	if err := pool.QueryRow(context.Background(),
		`SELECT record_version FROM install_appointment WHERE install_appointment_uuid=$1`, uuid).Scan(&v); err != nil {
		t.Errorf("read version: %v", err)
	}
	return v
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
	var updated atomic.Int32
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				crew := []CrewInput{{EmployeeID: emps[(i+1)%3], Role: CrewLead}, {EmployeeID: emps[(i+2)%3], Role: CrewMemberRole}}
				_, err = Update(ctx, pool, loc, time.Now(), appt.ID,
					UpdateInput{Crew: &crew, AcknowledgeConflicts: true, ExpectedVersion: currentVersion(t, pool, appt.ID)}, owner, allVisible)
				if err == nil {
					updated.Add(1)
				}
			} else {
				in := createInput(jobs[i], emps[i%3], "08:00", "12:00")
				in.Crew = append(in.Crew, CrewInput{EmployeeID: emps[(i+1)%3], Role: CrewMemberRole})
				in.AcknowledgeConflicts = true
				_, err = Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
			}
			// Stale versions and no-op crews are expected under contention; anything else (deadlock, SQL error) is not.
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

	won := int(updated.Load())
	require.GreaterOrEqual(t, won, 1, "at least one update must win")
	assert.Equal(t, appt.Version+won, currentVersion(t, pool, appt.ID))
	var reassigns, leads, members int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM install_appointment_history h
		JOIN install_appointment ia ON ia.install_appointment_id = h.install_appointment_id
		WHERE ia.install_appointment_uuid = $1 AND h.action = $2`, appt.ID, ActionReassign).Scan(&reassigns))
	assert.Equal(t, won, reassigns)
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE crew_role='lead'), count(*) FILTER (WHERE crew_role='member')
		FROM install_appointment_crew iac JOIN install_appointment ia ON ia.install_appointment_id = iac.install_appointment_id
		WHERE ia.install_appointment_uuid = $1`, appt.ID).Scan(&leads, &members))
	assert.Equal(t, 1, leads)
	assert.Equal(t, 1, members)
}
