//go:build dbtest

package vendortransactions

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedScheduled inserts a payment in the given status with a release date.
func seedScheduled(t *testing.T, pool *pgxpool.Pool, vendorID int, status, date string, amount float64) seeded {
	t.Helper()
	s := seedDoc(t, pool, vendorID, docSeed{key: "vendor_payment", status: status, date: "2026-10-01", total: amount})
	_, err := pool.Exec(context.Background(),
		"UPDATE vendor_payment SET vendor_payment_scheduled_date = $1::date WHERE vendor_payment_id = $2", date, s.id)
	require.NoError(t, err)
	return s
}

func TestScheduled_ExcludesNonScheduledAndMatchesOutflow(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "sched", readAll()...)
	vUUID, vID := seedVendor(t, pool, "Sched")
	late := seedScheduled(t, pool, vID, "SCHD", "2026-12-01", 300)
	soon := seedScheduled(t, pool, vID, "SCHD", "2026-11-05", 120.5)
	seedScheduled(t, pool, vID, "VOID", "2026-11-02", 999)
	seedScheduled(t, pool, vID, "APPV", "2026-11-03", 888)
	seedScheduled(t, pool, vID, "SENT", "2026-11-04", 777)
	seedScheduled(t, pool, vID, "DRFT", "2026-11-04", 666)
	gone := seedScheduled(t, pool, vID, "SCHD", "2026-11-06", 555)
	softDelete(t, pool, "vendor_payment", gone, a.employee)

	ctx := context.Background()
	sched, err := ListScheduled(ctx, pool, a.identityID, vUUID, MaxLimit)
	require.NoError(t, err)
	assert.False(t, sched.Denied)
	rows := sched.Rows
	require.Len(t, rows, 2)
	assert.Equal(t, soon.uuid, rows[0].ID, "soonest first")
	assert.Equal(t, late.uuid, rows[1].ID)

	ov, err := GetOverview(ctx, pool, a.identityID, vUUID, dbToday)
	require.NoError(t, err)
	require.NotNil(t, ov.Summary.ScheduledOutflow)
	require.NotNil(t, ov.Summary.ScheduledCount)
	var sum float64
	for _, r := range rows {
		sum += r.Amount
	}
	assert.InDelta(t, sum, *ov.Summary.ScheduledOutflow, 0.001)
	assert.InDelta(t, 420.5, *ov.Summary.ScheduledOutflow, 0.001)
	assert.Equal(t, len(rows), *ov.Summary.ScheduledCount)

	one, err := ListScheduled(ctx, pool, a.identityID, vUUID, 1)
	require.NoError(t, err)
	assert.Len(t, one.Rows, 1)

	nb := seedActor(t, pool, "schednb", readAll("vendor_payment")...)
	none, err := ListScheduled(ctx, pool, nb.identityID, vUUID, 0)
	require.NoError(t, err)
	assert.Empty(t, none.Rows)
	assert.NotNil(t, none.Rows, "denied serialises as [] not null")
	assert.True(t, none.Denied)
	ov, err = GetOverview(ctx, pool, nb.identityID, vUUID, dbToday)
	require.NoError(t, err)
	assert.Nil(t, ov.Summary.ScheduledOutflow)
	assert.Nil(t, ov.Summary.ScheduledCount)
}
