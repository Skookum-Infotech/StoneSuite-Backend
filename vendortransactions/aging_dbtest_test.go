//go:build dbtest

package vendortransactions

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAging_BucketsSumToOpenBalance(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "aging", readAll()...)
	vUUID, vID := seedVendor(t, pool, "Aging")
	// dbToday is 2026-10-31.
	for _, d := range []struct{ due, status string }{
		{"2026-10-31", "APPV"}, // due today: current
		{"2026-10-30", "APPV"}, // 1 day: 1-30
		{"2026-10-01", "APPV"}, // 30 days: 1-30
		{"2026-09-30", "PART"}, // 31 days: 31-60
		{"2026-09-01", "APPV"}, // 60 days: 31-60
		{"2026-08-31", "APPV"}, // 61 days: 61+
	} {
		seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: d.status, date: "2026-07-01", due: d.due, total: 100, balance: 100})
	}
	seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "VOID", date: "2026-07-01", due: "2026-01-01", total: 999, balance: 999})

	ctx := context.Background()
	ag, err := GetAging(ctx, pool, a.identityID, vUUID, dbToday)
	require.NoError(t, err)
	require.Len(t, ag.Buckets, 4)
	for i, want := range []string{AgingCurrent, Aging1To30, Aging31To60, Aging61Plus} {
		assert.Equal(t, want, ag.Buckets[i].Label)
		assert.Equal(t, 1+boolInt(i == 1 || i == 2), ag.Buckets[i].Count, want)
	}
	ov, err := GetOverview(ctx, pool, a.identityID, vUUID, dbToday)
	require.NoError(t, err)
	require.NotNil(t, ov.Summary.OpenBalance)
	assert.InDelta(t, *ov.Summary.OpenBalance, ag.Total, 0.001)
	var sum float64
	for _, b := range ag.Buckets {
		sum += b.Amount
	}
	assert.InDelta(t, *ov.Summary.OpenBalance, sum, 0.001)

	nb := seedActor(t, pool, "agingnb", readAll("vendor_bill")...)
	none, err := GetAging(ctx, pool, nb.identityID, vUUID, dbToday)
	require.NoError(t, err)
	assert.Nil(t, none.Buckets, "unreadable bills yield no buckets")
	assert.True(t, none.Denied)
	assert.False(t, ag.Denied)
}

func TestAging_TwoTimezones(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "agingtz", readAll()...)
	vUUID, vID := seedVendor(t, pool, "AgingTZ")
	seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "APPV", date: "2026-07-01", due: "2026-10-31", total: 50, balance: 50})

	// The same instant is Oct 31 in one zone and Nov 1 in another; the bill is
	// current on the former and one day late on the latter.
	for _, tc := range []struct {
		name   string
		today  time.Time
		bucket int
	}{
		{"tenant day Oct 31", time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), 0},
		{"tenant day Nov 1", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag, err := GetAging(context.Background(), pool, a.identityID, vUUID, tc.today)
			require.NoError(t, err)
			assert.InDelta(t, 50, ag.Buckets[tc.bucket].Amount, 0.001)
			assert.InDelta(t, 50, ag.Total, 0.001)
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
