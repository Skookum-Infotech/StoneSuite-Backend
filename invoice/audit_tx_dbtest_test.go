//go:build dbtest

package invoice_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
	"stonesuite-backend/invoice"
	"stonesuite-backend/workflow"
)

// auditedCtx mimics controllers.auditCtx for a store call.
func auditedCtx() context.Context {
	return workflow.WithAuditRequest(context.Background(), workflow.AuditRequest{
		IPAddress: "203.0.113.7", SessionID: "sess-1", AppVersion: "test",
	})
}

func invoiceAuditCount(t *testing.T, pool *pgxpool.Pool, uuid, action string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_logs WHERE resource = 'invoice' AND resource_id = $1 AND action = $2`,
		uuid, action).Scan(&n))
	return n
}

func TestAuditTx_CreateReplayAndUpdate(t *testing.T) {
	pool := testPool(t)
	ctx := auditedCtx()
	cust, item := seedCustomerAndItem(t)
	in := baseInput(cust, item)
	in.IdempotencyKey = "key-audit-" + cust
	in.RequestHash = idempotency.HashRequest([]byte("audit-body"))

	first, err := invoice.Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, invoiceAuditCount(t, pool, first.ID, "create"))

	// An idempotent replay returns the first invoice and audits nothing.
	second, err := invoice.Create(ctx, pool, in, 1)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	assert.Equal(t, 1, invoiceAuditCount(t, pool, first.ID, "create"))

	// Update writes one "update" row carrying the old value.
	upd := invoice.UpdateInvoiceInput{
		Memo:  "changed",
		Items: []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: item, Quantity: 2, UnitPrice: 10}},
	}
	_, err = invoice.Update(ctx, pool, first.ID, upd, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, invoiceAuditCount(t, pool, first.ID, "update"))
	var oldSet bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT old_value IS NOT NULL FROM audit_logs
		 WHERE resource = 'invoice' AND resource_id = $1 AND action = 'update'`, first.ID).Scan(&oldSet))
	assert.True(t, oldSet)

	// Without an AuditRequest on ctx the store writes no audit row.
	_, err = invoice.Update(context.Background(), pool, first.ID, upd, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, invoiceAuditCount(t, pool, first.ID, "update"))
}
