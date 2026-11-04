//go:build dbtest

package installschedule

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHistory_ListsRowsNewestFirstWithoutConflictDetail(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "histowner")
	_, _, emp := seedUserEmployee(t, pool, "histcrew")
	_, jobUUID := seedJob(t, pool, owner)

	res, err := Create(ctx, pool, loc, time.Now(), createInput(jobUUID, emp, "08:00", "12:00"), owner, allVisible)
	require.NoError(t, err)
	_, err = Cancel(ctx, pool, res.Appointment.ID, res.Appointment.Version, "weather", owner, "")
	require.NoError(t, err)

	got, err := History(ctx, pool, res.Appointment.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, ActionCancel, got[0].Action)
	assert.Equal(t, "weather", got[0].Reason)
	assert.Equal(t, ActionCreate, got[1].Action)
	assert.Equal(t, owner, got[1].ActorEmployeeID)
	assert.NotEmpty(t, got[1].ActorName)

	_, err = History(ctx, pool, "00000000-0000-0000-0000-000000000000")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCreate_ReplayRequiresVisibility(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, owner := seedUserEmployee(t, pool, "replayowner")
	_, jobUUID := seedJob(t, pool, owner)
	in := createInput(jobUUID, owner, "08:00", "12:00")
	in.RequestID = "5b1d9a36-0a3c-4c5e-9d0e-3f7a1c2b4d60"

	_, err := Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
	require.NoError(t, err)

	hidden := func(context.Context, Querier, string) bool { return false }
	_, err = Create(ctx, pool, loc, time.Now(), in, owner, hidden)
	assert.ErrorIs(t, err, ErrNotFound)

	res, err := Create(ctx, pool, loc, time.Now(), in, owner, allVisible)
	require.NoError(t, err)
	assert.True(t, res.Replayed)
}
