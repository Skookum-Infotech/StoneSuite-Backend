//go:build dbtest

package requisition

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
	"stonesuite-backend/purchaseorder"
)

func baseCreateInput(item string) CreateRequisitionInput {
	return CreateRequisitionInput{requisitionFields: requisitionFields{
		Items: []LineInput{{LineNumber: 1, InventoryItemUUID: item, Quantity: 1}},
	}}
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, item := seedVendorAndItem(t, pool)
	in := baseCreateInput(item)
	in.IdempotencyKey = "reqn-replay-" + item
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
		`SELECT COUNT(*) FROM requisition WHERE requisition_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, item := seedVendorAndItem(t, pool)
	in := baseCreateInput(item)
	in.IdempotencyKey = "reqn-reuse-" + item
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
	_, item := seedVendorAndItem(t, pool)
	rq, err := Create(ctx, pool, baseCreateInput(item), 1)
	require.NoError(t, err)

	upd := func(v int) UpdateRequisitionInput {
		return UpdateRequisitionInput{
			requisitionFields: requisitionFields{
				Memo:  "edited",
				Items: []LineInput{{LineNumber: 1, InventoryItemUUID: item, Quantity: 1}},
			},
			ExpectedVersion: &v,
		}
	}
	_, err = Update(ctx, pool, rq.ID, upd(rq.RecordVersion+99), 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	after, err := Update(ctx, pool, rq.ID, upd(rq.RecordVersion), 1)
	require.NoError(t, err)
	assert.Equal(t, "edited", after.Memo)
	assert.Equal(t, rq.RecordVersion+1, after.RecordVersion)
}

func TestCreate_RejectsBadInput(t *testing.T) {
	pool := testPool(t)
	_, item := seedVendorAndItem(t, pool)

	tooMany := baseCreateInput(item)
	tooMany.Items = make([]LineInput, maxLineItems+1)
	badDate := baseCreateInput(item)
	badDate.NeededByDate = "not-a-date"

	for name, in := range map[string]CreateRequisitionInput{"too many lines": tooMany, "bad needed-by": badDate} {
		t.Run(name, func(t *testing.T) {
			_, err := Create(context.Background(), pool, in, 1)
			var ce ClientError
			assert.True(t, errors.As(err, &ce), "got %v", err)
		})
	}
}

func TestSoftDelete_RefusedWhenConverted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor, item := seedVendorAndItem(t, pool)
	rq, err := Create(ctx, pool, baseCreateInput(item), 1)
	require.NoError(t, err)
	_, err = Transition(ctx, pool, rq.ID, "APPV", 1)
	require.NoError(t, err)

	po, created, err := purchaseorder.ConvertFromRequisition(ctx, pool, rq.ID, vendor, 1)
	require.NoError(t, err)
	require.True(t, created)

	// Recall to draft so only the conversion gate (not the status gate) refuses.
	_, err = Transition(ctx, pool, rq.ID, "DRFT", 1)
	require.NoError(t, err)
	err = SoftDelete(ctx, pool, rq.ID, 1)
	assert.ErrorIs(t, err, ErrHasDependents)

	// Once the purchase order is gone the requisition can be deleted.
	require.NoError(t, purchaseorder.SoftDelete(ctx, pool, po.ID, 1))
	assert.NoError(t, SoftDelete(ctx, pool, rq.ID, 1))
}
