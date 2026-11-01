//go:build dbtest

package refund

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
	"stonesuite-backend/workflow"
)

func TestAuditTx_CreateAndReplay(t *testing.T) {
	pool := testPool(t)
	ctx := workflow.WithAuditRequest(context.Background(), workflow.AuditRequest{
		IPAddress: "203.0.113.7", SessionID: "sess-1", AppVersion: "test",
	})
	cust := seedCustomer(t, pool)
	in := CreateRefundInput{CustomerUUID: cust, MethodID: firstMethodID(t, pool), Amount: 40}
	in.IdempotencyKey = "rf-audit-" + cust
	in.RequestHash = idempotency.HashRequest([]byte("a"))

	count := func(id string) int {
		var n int
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM audit_logs WHERE resource = 'refund' AND resource_id = $1 AND action = 'create'`,
			id).Scan(&n))
		return n
	}
	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, count(first.ID))

	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	assert.Equal(t, 1, count(first.ID))
}
