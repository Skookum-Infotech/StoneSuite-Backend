package vendortransactions

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListScheduled_RejectsBadLimit(t *testing.T) {
	for _, limit := range []int{-1, MaxLimit + 1} {
		_, err := ListScheduled(context.Background(), nil, "", "", limit)
		var invalid *InvalidParamError
		require.ErrorAs(t, err, &invalid)
		assert.Equal(t, "limit", invalid.Param)
	}
}

func TestBuildScheduledSQL(t *testing.T) {
	pay := granted{Source: vendorPayment(), own: true}
	today := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)

	sql, args := buildScheduledTotalSQL(pay, 7, 3, today)
	assert.Contains(t, sql, "status_code = 'SCHD'")
	assert.Equal(t, []any{7, 3}, args[:2])

	sql, args = buildScheduledListSQL(pay, 7, 3, today, 5)
	assert.Contains(t, sql, "status_code = 'SCHD'")
	assert.Contains(t, sql, "ORDER BY v.vendor_payment_scheduled_date NULLS LAST, x.id LIMIT")
	assert.Equal(t, 5, args[len(args)-1])
}
