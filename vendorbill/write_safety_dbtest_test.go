//go:build dbtest

package vendorbill

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func wsInput(vendor string) CreateVendorBillInput {
	return CreateVendorBillInput{
		VendorUUID: vendor,
		vendorBillFields: vendorBillFields{
			Items: []LineInput{{LineNumber: 1, Description: "Slab", Quantity: 1, UnitPrice: 10}},
		},
	}
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := wsInput(seedVendor(t, pool))
	in.IdempotencyKey = "vb-key-replay-" + in.VendorUUID
	in.RequestHash = idempotency.HashRequest([]byte("body-a"))

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
		`SELECT COUNT(*) FROM vendor_bill WHERE vendor_bill_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := wsInput(seedVendor(t, pool))
	in.IdempotencyKey = "vb-key-reuse-" + in.VendorUUID
	in.RequestHash = idempotency.HashRequest([]byte("body-a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bill, err := Create(ctx, pool, wsInput(seedVendor(t, pool)), 1)
	require.NoError(t, err)

	upd := func(v int) UpdateVendorBillInput {
		return UpdateVendorBillInput{
			vendorBillFields: vendorBillFields{
				Memo:  "edited",
				Items: []LineInput{{LineNumber: 1, Description: "Slab", Quantity: 1, UnitPrice: 10}},
			},
			ExpectedVersion: &v,
		}
	}
	_, err = Update(ctx, pool, bill.ID, upd(bill.RecordVersion+99), 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	after, err := Update(ctx, pool, bill.ID, upd(bill.RecordVersion), 1)
	require.NoError(t, err)
	assert.Equal(t, "edited", after.Memo)
}

func TestCreate_RejectsBadInput(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)

	tooMany := wsInput(vendor)
	tooMany.Items = make([]LineInput, maxLineItems+1)
	dueEarly := wsInput(vendor)
	dueEarly.BillDate, dueEarly.DueDate = "2026-02-01", "2026-01-01"

	for name, in := range map[string]CreateVendorBillInput{"501 lines": tooMany, "due before bill": dueEarly} {
		t.Run(name, func(t *testing.T) {
			_, err := Create(ctx, pool, in, 1)
			var ce ClientError
			assert.True(t, errors.As(err, &ce), "got %v", err)
		})
	}
}

func TestSoftDelete_RefusedWithLivePayment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bill, err := Create(ctx, pool, wsInput(seedVendor(t, pool)), 1)
	require.NoError(t, err)

	// A draft bill never takes payments through the API; seed the ledger row
	// directly to exercise the gate.
	_, err = pool.Exec(ctx, `
		INSERT INTO vendor_bill_payment (vendor_bill_id, amount, paid_at)
		SELECT vendor_bill_id, 5, CURRENT_DATE FROM vendor_bill WHERE vendor_bill_uuid = $1`, bill.ID)
	require.NoError(t, err)

	err = SoftDelete(ctx, pool, bill.ID, 1)
	var ce ClientError
	assert.True(t, errors.As(err, &ce), "got %v", err)
	_, err = Get(ctx, pool, bill.ID)
	assert.NoError(t, err, "bill must survive a refused delete")
}

func TestSoftDelete_DeletesCleanDraftAndRepeatIsNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bill, err := Create(ctx, pool, wsInput(seedVendor(t, pool)), 1)
	require.NoError(t, err)

	require.NoError(t, SoftDelete(ctx, pool, bill.ID, 1))
	assert.ErrorIs(t, SoftDelete(ctx, pool, bill.ID, 1), ErrNotFound)
}
