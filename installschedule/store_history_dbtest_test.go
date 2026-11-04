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
