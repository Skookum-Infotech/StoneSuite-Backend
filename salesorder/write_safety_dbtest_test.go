//go:build dbtest

package salesorder

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func keyedInput(cust, item, key, body string) CreateOrderInput {
	in := dupInput(cust, item, "", false)
	in.InstallDate = ""
	in.IdempotencyKey = key
	in.RequestHash = idempotency.HashRequest([]byte(body))
	return in
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t, pool)
	in := keyedInput(cust, item, "key-replay-"+cust, "body-a")

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM request_idempotency WHERE idem_key = $1`, in.IdempotencyKey).Scan(&n))
	assert.Equal(t, 1, n)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM sales_order WHERE sales_order_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t, pool)
	in := keyedInput(cust, item, "key-reuse-"+cust, "body-a")
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t, pool)
	so, err := Create(ctx, pool, dupInput(cust, item, "", false), 1)
	require.NoError(t, err)

	upd := func(v int) UpdateOrderInput {
		in := UpdateOrderInput{ExpectedVersion: &v}
		in.Memo = "edited"
		in.Items = []LineInput2{{LineNumber: 1, InventoryItemUUID: item, Quantity: 1}}
		return in
	}
	_, err = Update(ctx, pool, so.ID, upd(so.RecordVersion+99), 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	after, err := Update(ctx, pool, so.ID, upd(so.RecordVersion), 1)
	require.NoError(t, err)
	assert.Equal(t, "edited", after.Memo)
	assert.Equal(t, so.RecordVersion+1, after.RecordVersion)
}

func TestCreate_RejectsTooManyLines(t *testing.T) {
	pool := testPool(t)
	cust, item := seedCustomerAndItem(t, pool)
	in := dupInput(cust, item, "", false)
	in.Items = make([]LineInput2, maxLineItems+1)
	_, err := Create(context.Background(), pool, in, 1)
	assert.True(t, IsClientError(err), "got %v", err)
}

func TestSoftDelete_RefusedWithLiveInvoice(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t, pool)
	so, err := Create(ctx, pool, dupInput(cust, item, "", false), 1)
	require.NoError(t, err)

	var invoiceID int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO invoice (record_type, invoice_status, invoice_customer_id, invoice_sales_order_id)
		SELECT rt.record_type_id, rs.record_status_id, c.customer_id, so.sales_order_id
		FROM lkp_record_type rt
		JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id
		CROSS JOIN customer c CROSS JOIN sales_order so
		WHERE rt.record_type_code = 'INVC' AND c.customer_uuid = $1 AND so.sales_order_uuid = $2
		ORDER BY rs.record_status_id LIMIT 1
		RETURNING invoice_id`, cust, so.ID).Scan(&invoiceID))

	err = SoftDelete(ctx, pool, so.ID, 1)
	assert.ErrorIs(t, err, ErrHasDependents)
	_, err = Get(ctx, pool, so.ID)
	assert.NoError(t, err, "order must survive a refused delete")

	_, err = pool.Exec(ctx, `UPDATE invoice SET invoice_deleted_at = NOW(), invoice_deleted_by = 1 WHERE invoice_id = $1`, invoiceID)
	require.NoError(t, err)
	assert.NoError(t, SoftDelete(ctx, pool, so.ID, 1))
}
