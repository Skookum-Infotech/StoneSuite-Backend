//go:build dbtest

package payment

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuickPayIdempotencyKey(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, invUUID := seedSentInvoice(t, pool, 100)

	first, err := QuickPay(ctx, pool, invUUID, 40, "key-1", 1)
	require.NoError(t, err)
	require.Equal(t, 40.0, first.AmountPaid)

	replay, err := QuickPay(ctx, pool, invUUID, 40, "key-1", 1)
	require.NoError(t, err)
	require.Equal(t, 40.0, replay.AmountPaid, "replay must not double-pay")

	other, err := QuickPay(ctx, pool, invUUID, 10, "key-2", 1)
	require.NoError(t, err)
	require.Equal(t, 50.0, other.AmountPaid)
}

func TestQuickPayFailedApplyLeavesNoOrphan(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, invUUID := seedSentInvoice(t, pool, 100)

	var before, after int
	count := `SELECT COUNT(*) FROM payment WHERE payment_memo = $1`
	require.NoError(t, pool.QueryRow(ctx, count, quickPayMemo).Scan(&before))
	_, err := QuickPay(ctx, pool, invUUID, 500, "", 1)
	require.Error(t, err)
	require.NoError(t, pool.QueryRow(ctx, count, quickPayMemo).Scan(&after))
	require.Equal(t, before, after)
}
