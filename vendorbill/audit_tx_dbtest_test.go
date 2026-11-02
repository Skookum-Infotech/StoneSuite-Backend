//go:build dbtest

package vendorbill

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

func vbAuditCount(t *testing.T, pool *pgxpool.Pool, uuid, action string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_logs WHERE resource = 'vendor_bill' AND resource_id = $1 AND action = $2`,
		uuid, action).Scan(&n))
	return n
}

func TestAuditTx_CreateReplayAndRecordPayment(t *testing.T) {
	pool := testPool(t)
	ctx := auditedCtx()
	in := wsInput(seedVendor(t, pool))
	in.IdempotencyKey = "vb-audit-" + in.VendorUUID
	in.RequestHash = idempotency.HashRequest([]byte("audit-body"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, vbAuditCount(t, pool, first.ID, "create"))

	// An idempotent replay returns the first bill and audits nothing.
	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	assert.Equal(t, 1, vbAuditCount(t, pool, first.ID, "create"))

	// No approvers are configured, so PAPV auto-skips straight to APPV.
	_, err = Transition(ctx, pool, first.ID, "PAPV", 1)
	require.NoError(t, err)
	assert.Equal(t, 1, vbAuditCount(t, pool, first.ID, "transition"))

	pay := RecordPaymentInput{Amount: 4, IdempotencyKey: "vb-pay-" + first.ID}
	_, err = RecordPayment(ctx, pool, first.ID, pay, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, vbAuditCount(t, pool, first.ID, "record_payment"))
	var oldSet bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT old_value IS NOT NULL FROM audit_logs
		 WHERE resource = 'vendor_bill' AND resource_id = $1 AND action = 'record_payment'`, first.ID).Scan(&oldSet))
	assert.True(t, oldSet)

	// A replayed payment key records nothing and audits nothing.
	_, err = RecordPayment(ctx, pool, first.ID, pay, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, vbAuditCount(t, pool, first.ID, "record_payment"))

	// Without an AuditRequest on ctx the store writes no audit row.
	_, err = RecordPayment(context.Background(), pool, first.ID, RecordPaymentInput{Amount: 1}, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, vbAuditCount(t, pool, first.ID, "record_payment"))
}
