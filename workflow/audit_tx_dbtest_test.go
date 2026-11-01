//go:build dbtest

package workflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	txAuditResource = "audit_tx_probe"
	txAuditAction   = "create"
)

func countProbeAudit(t *testing.T, ctx context.Context, q Querier, resourceID string) int {
	t.Helper()
	var n int
	require.NoError(t, q.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit_logs WHERE resource = $1 AND resource_id = $2`,
		txAuditResource, resourceID).Scan(&n))
	return n
}

func TestLogAuditTx_FollowsTransaction(t *testing.T) {
	pool := testPool(t)
	ctx := WithAuditRequest(context.Background(), AuditRequest{IPAddress: "bad-ip", SessionID: "s1", AppVersion: "t"})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE resource = $1`, txAuditResource)
	})

	// Rolled back: no audit row.
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, LogAuditTx(ctx, tx, txAuditAction, txAuditResource, "rb", "probe", nil, map[string]any{"k": 1}, nil))
	require.NoError(t, tx.Rollback(ctx))
	assert.Equal(t, 0, countProbeAudit(t, ctx, pool, "rb"))

	// Committed: exactly one row, bad IP stored as NULL.
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, LogAuditTx(ctx, tx, txAuditAction, txAuditResource, "cm", "probe", nil, map[string]any{"k": 1}, nil))
	require.NoError(t, tx.Commit(ctx))
	assert.Equal(t, 1, countProbeAudit(t, ctx, pool, "cm"))
	var ipNull bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT ip_address IS NULL FROM audit_logs WHERE resource = $1 AND resource_id = 'cm'`, txAuditResource).Scan(&ipNull))
	assert.True(t, ipNull)
}

func TestLogAuditTx_FailingObserverDoesNotAbortTx(t *testing.T) {
	pool := testPool(t)
	ctx := WithAuditRequest(context.Background(), AuditRequest{})
	t.Cleanup(resetAuditObservers)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE resource = $1`, txAuditResource)
	})
	resetAuditObservers()
	RegisterAuditObserver(func(ctx context.Context, q Querier, ev AuditEvent) {
		_, _ = q.Exec(ctx, `SELECT * FROM table_that_does_not_exist_zz`)
	})

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, LogAuditTx(ctx, tx, txAuditAction, txAuditResource, "obs", "probe", nil, nil, nil))
	// The tx is still usable and commits.
	assert.Equal(t, 1, countProbeAudit(t, ctx, tx, "obs"))
	require.NoError(t, tx.Commit(ctx))
	assert.Equal(t, 1, countProbeAudit(t, ctx, pool, "obs"))
}
