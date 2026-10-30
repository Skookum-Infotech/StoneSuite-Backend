//go:build dbtest

package vendorpayment

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
	in := CreateVendorPaymentInput{VendorUUID: seedVendor(t, pool), MethodID: activeMethodID(t, pool), Amount: 50}
	in.IdempotencyKey = "vp-audit-" + in.VendorUUID
	in.RequestHash = idempotency.HashRequest([]byte("audit-body"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)

	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_logs WHERE resource = 'vendor_payment' AND resource_id = $1 AND action = 'create'`,
		first.ID).Scan(&n))
	assert.Equal(t, 1, n)
}
