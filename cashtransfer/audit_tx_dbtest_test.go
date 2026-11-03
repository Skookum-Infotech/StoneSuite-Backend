//go:build dbtest

package cashtransfer

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

func ctAuditCount(t *testing.T, pool *pgxpool.Pool, uuid, action string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_logs WHERE resource = 'cash_transfer' AND resource_id = $1 AND action = $2`,
		uuid, action).Scan(&n))
	return n
}

func TestAuditTx_CreatePostReverse(t *testing.T) {
	pool := testPool(t)
	ctx := auditedCtx()
	from := seedAccount(t, pool, "9860", "AuditFrom", true, true)
	to := seedAccount(t, pool, "9861", "AuditTo", true, true)
	in := CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 20,
		IdempotencyKey: "ct-audit-key", RequestHash: idempotency.HashRequest([]byte("audit-body"))}

	ct, err := Create(ctx, pool, in, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, ctAuditCount(t, pool, ct.ID, "create"))

	// An idempotent replay returns the first transfer and audits nothing.
	again, err := Create(ctx, pool, in, 0)
	require.NoError(t, err)
	require.Equal(t, ct.ID, again.ID)
	assert.Equal(t, 1, ctAuditCount(t, pool, ct.ID, "create"))

	_, err = Transition(ctx, pool, ct.ID, approvedStatusCode, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, ctAuditCount(t, pool, ct.ID, "transition"))

	posted, err := Post(ctx, pool, ct.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, ctAuditCount(t, pool, ct.ID, "post"))
	var entries int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM journal_entry WHERE source_type = $1 AND source_id = $2`,
		sourceType, posted.ID).Scan(&entries))
	assert.Equal(t, 1, entries)

	// A refused second post rolls back and writes no audit row.
	_, err = Post(ctx, pool, ct.ID, 0)
	require.ErrorIs(t, err, ErrAlreadyPosted)
	assert.Equal(t, 1, ctAuditCount(t, pool, ct.ID, "post"))

	_, err = Reverse(ctx, pool, ct.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, ctAuditCount(t, pool, ct.ID, "reverse"))
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM journal_entry WHERE source_type = $1 AND source_id = $2`,
		sourceType, posted.ID).Scan(&entries))
	assert.Equal(t, 2, entries)
}
