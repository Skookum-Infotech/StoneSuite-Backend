//go:build dbtest

package installschedule

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			_, err := Create(ctx, pool, loc, time.Now(), createInput(jobs[i], emp, "08:00", "12:00"), owner, allVisible)
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

func TestCreate_ConcurrentSameRequestID(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	jobID, jobUUID := seedJob(t, pool, owner)
	in := createInput(jobUUID, emp, "08:00", "12:00")
	in.RequestID = "0b8f6c2e-5a1d-4c7e-8d33-2f9a4e6b1c07"

	const workers = 5
	var wg sync.WaitGroup
	var fresh, replayed atomic.Int32
	ids := make([]string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			ids[i] = res.Appointment.ID
			if res.Replayed {
				replayed.Add(1)
			} else {
				fresh.Add(1)
			}
		}(i)
	}
	wg.Wait()
	assert.EqualValues(t, 1, fresh.Load())
	assert.EqualValues(t, workers-1, replayed.Load())
	for _, id := range ids {
		assert.Equal(t, ids[0], id)
	}
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM install_appointment WHERE fabrication_job_id=$1`, jobID).Scan(&n))
	assert.Equal(t, 1, n)
}
