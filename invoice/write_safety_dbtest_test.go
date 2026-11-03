//go:build dbtest

package invoice_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
	"stonesuite-backend/invoice"
)

func seedCustomerAndItem(t *testing.T) (custUUID, itemUUID string) {
	t.Helper()
	custUUID, invUUID := seedSentInvoice(t, testPool(t), 10)
	_ = invUUID
	var item string
	require.NoError(t, testPool(t).QueryRow(context.Background(),
		`SELECT inventory_item_uuid FROM inventory_item ORDER BY inventory_item_id DESC LIMIT 1`).Scan(&item))
	return custUUID, item
}

func baseInput(cust, item string) invoice.CreateInvoiceInput {
	return invoice.CreateInvoiceInput{
		CustomerUUID: cust,
		Items:        []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: item, Quantity: 1, UnitPrice: 10}},
	}
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t)
	in := baseInput(cust, item)
	in.IdempotencyKey = "key-replay-" + cust
	in.RequestHash = idempotency.HashRequest([]byte("body-a"))

	first, err := invoice.Create(ctx, pool, in, 1)
	require.NoError(t, err)
	second, err := invoice.Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM request_idempotency WHERE idem_key = $1`, in.IdempotencyKey).Scan(&n))
	assert.Equal(t, 1, n)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM invoice WHERE invoice_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t)
	in := baseInput(cust, item)
	in.IdempotencyKey = "key-reuse-" + cust
	in.RequestHash = idempotency.HashRequest([]byte("body-a"))
	_, err := invoice.Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = invoice.Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t)
	inv, err := invoice.Create(ctx, pool, baseInput(cust, item), 1)
	require.NoError(t, err)

	upd := func(v int) invoice.UpdateInvoiceInput {
		return invoice.UpdateInvoiceInput{
			Memo:            "edited",
			Items:           []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: item, Quantity: 1, UnitPrice: 10}},
			ExpectedVersion: &v,
		}
	}
	_, err = invoice.Update(ctx, pool, inv.ID, upd(inv.RecordVersion+99), 1)
	assert.ErrorIs(t, err, invoice.ErrVersionConflict)

	after, err := invoice.Update(ctx, pool, inv.ID, upd(inv.RecordVersion), 1)
	require.NoError(t, err)
	assert.Equal(t, "edited", after.Memo)
}

func TestCreate_RejectsBadInput(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t)

	tooMany := baseInput(cust, item)
	tooMany.Items = make([]invoice.InvoiceLineInput, 501)
	dueEarly := baseInput(cust, item)
	dueEarly.InvoiceDate, dueEarly.DueDate = "2026-02-01", "2026-01-01"

	tests := []struct {
		name string
		in   invoice.CreateInvoiceInput
	}{
		{"501 lines", tooMany},
		{"due before invoice date", dueEarly},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := invoice.Create(ctx, pool, tc.in, 1)
			var ce invoice.ClientError
			assert.True(t, errors.As(err, &ce), "got %v", err)
		})
	}
}

func TestSoftDelete_RefusedWithLiveSettlement(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	applyPayment(t, pool, custUUID, invUUID, 40)

	err := invoice.SoftDelete(ctx, pool, invUUID, 1)
	var ce invoice.ClientError
	assert.True(t, errors.As(err, &ce), "got %v", err)
	assertInvoiceStatus(t, pool, invUUID, "PART")
}
