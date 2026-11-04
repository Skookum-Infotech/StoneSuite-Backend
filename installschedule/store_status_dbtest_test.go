//go:build dbtest

package installschedule

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCancel_RequiresScheduled(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, jobUUID := seedJob(t, pool, owner)
	appt := bookAt(t, pool, jobUUID, emp, owner, "08:00", "12:00")

	var ve *ValidationError
	_, err := Cancel(ctx, pool, appt.ID, appt.Version, "", owner, "")
	require.ErrorAs(t, err, &ve)
	_, err = Cancel(ctx, pool, appt.ID, appt.Version, strings.Repeat("é", MaxReason+1), owner, "")
	require.ErrorAs(t, err, &ve)

	_, err = Cancel(ctx, pool, appt.ID, appt.Version+5, "why", owner, "")
	var sv *StaleVersionError
	require.ErrorAs(t, err, &sv)
	assert.Equal(t, appt.Version, sv.CurrentVersion)

	res, err := Cancel(ctx, pool, appt.ID, appt.Version, strings.Repeat("é", MaxReason), owner, "")
	require.NoError(t, err)
	assert.Equal(t, []string{ActionCancel}, res.Actions)
	assert.Equal(t, StatusCancelled, res.After.Status)
	assert.Equal(t, appt.Version+1, res.After.Version)
	require.NotNil(t, res.After.CancelReason)

	_, err = Cancel(ctx, pool, appt.ID, res.After.Version, "again", owner, "")
	assert.ErrorIs(t, err, ErrInvalidStatus)
}

func TestComplete_NotStartedAndStale(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, jobUUID := seedJob(t, pool, owner)
	appt := bookAt(t, pool, jobUUID, emp, owner, "08:00", "12:00")
	start := time.Date(2032, 6, 10, 8, 0, 0, 0, loc)

	_, err := Complete(ctx, pool, appt.ID, appt.Version, owner, start.Add(-time.Minute), "")
	assert.ErrorIs(t, err, ErrNotStarted)

	_, err = Complete(ctx, pool, appt.ID, appt.Version+3, owner, start, "")
	assert.ErrorIs(t, err, ErrStaleVersion)

	res, err := Complete(ctx, pool, appt.ID, appt.Version, owner, start, "")
	require.NoError(t, err)
	assert.Equal(t, []string{ActionComplete}, res.Actions)
	assert.Equal(t, StatusCompleted, res.After.Status)
	assert.Equal(t, appt.Version+1, res.After.Version)

	_, err = Complete(ctx, pool, appt.ID, res.After.Version, owner, start, "")
	assert.ErrorIs(t, err, ErrInvalidStatus)
}

func TestCancelled_DoesNotConflict(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "owner")
	_, _, emp := seedUserEmployee(t, pool, "crew")
	_, jobA := seedJob(t, pool, owner)
	_, jobB := seedJob(t, pool, owner)
	appt := bookAt(t, pool, jobA, emp, owner, "08:00", "12:00")
	_, err := Cancel(ctx, pool, appt.ID, appt.Version, "weather", owner, "")
	require.NoError(t, err)

	_, err = Create(ctx, pool, loc, time.Now(), createInput(jobB, emp, "09:00", "11:00"), owner, allVisible)
	require.NoError(t, err)
}
