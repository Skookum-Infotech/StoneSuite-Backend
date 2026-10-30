//go:build dbtest

package payment

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
	"stonesuite-backend/workflow"
)

// auditedCtx mimics controllers.auditCtx for a store call.
func auditedCtx() context.Context {
	return workflow.WithAuditRequest(context.Background(), workflow.AuditRequest{
		IPAddress: "203.0.113.7", SessionID: "sess-1", AppVersion: "test",
	})
}

func paymentAuditCount(t *testing.T, pool *pgxpool.Pool, uuid, action string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_logs WHERE resource = 'payment' AND resource_id = $1 AND action = $2`,
		uuid, action).Scan(&n))
	return n
}

func TestAuditTx_CreateReplayAndApply(t *testing.T) {
	pool := testPool(t)
	ctx := auditedCtx()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	in := CreatePaymentInput{CustomerUUID: custUUID, MethodID: firstMethodID(t, pool), Amount: 100}
	in.IdempotencyKey = "pay-audit-" + custUUID
	in.RequestHash = idempotency.HashRequest([]byte("audit-body"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, paymentAuditCount(t, pool, first.ID, "create"))

	// An idempotent replay returns the first payment and audits nothing.
	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	assert.Equal(t, 1, paymentAuditCount(t, pool, first.ID, "create"))

	// Apply writes one "apply" row.
	_, err = Apply(ctx, pool, first.ID, invUUID, 40, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, paymentAuditCount(t, pool, first.ID, "apply"))

	// Without an AuditRequest on ctx the store writes no audit row.
	_, err = Apply(context.Background(), pool, first.ID, invUUID, 10, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, paymentAuditCount(t, pool, first.ID, "apply"))
}
