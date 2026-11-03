//go:build dbtest

package itemreceipt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func irInput(poUUID, lineUUID string, qty float64) CreateItemReceiptInput {
	return CreateItemReceiptInput{
		PurchaseOrderUUID: poUUID,
		itemReceiptFields: itemReceiptFields{
			Items: []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: lineUUID, QtyReceived: qty}},
		},
	}
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, lineUUID, _ := seedSentPO(t, pool, 100)
	in := irInput(poUUID, lineUUID, 10)
	in.IdempotencyKey = "ir-replay-" + poUUID
	in.RequestHash = idempotency.HashRequest([]byte("body-a"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM item_receipt ir JOIN purchase_order po ON po.purchase_order_id = ir.purchase_order_id
		 WHERE po.purchase_order_uuid = $1`, poUUID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, lineUUID, _ := seedSentPO(t, pool, 100)
	in := irInput(poUUID, lineUUID, 10)
	in.IdempotencyKey = "ir-reuse-" + poUUID
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
	poUUID, lineUUID, _ := seedSentPO(t, pool, 100)
	r, err := Create(ctx, pool, irInput(poUUID, lineUUID, 10), 1)
	require.NoError(t, err)

	upd := func(v int) UpdateItemReceiptInput {
		return UpdateItemReceiptInput{
			itemReceiptFields: itemReceiptFields{
				PackingSlip: "PS-edited",
				Items:       []LineInput{{LineNumber: 1, PurchaseOrderItemUUID: lineUUID, QtyReceived: 12}},
			},
			ExpectedVersion: &v,
		}
	}
	_, err = Update(ctx, pool, r.ID, upd(r.RecordVersion+99), 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	after, err := Update(ctx, pool, r.ID, upd(r.RecordVersion), 1)
	require.NoError(t, err)
	assert.Equal(t, "PS-edited", after.PackingSlip)
	assert.Equal(t, r.RecordVersion+1, after.RecordVersion)
}

func TestCreate_RejectsBadQuantity(t *testing.T) {
	pool := testPool(t)
	poUUID, lineUUID, _ := seedSentPO(t, pool, 100)
	in := irInput(poUUID, lineUUID, 1e12)
	_, err := Create(context.Background(), pool, in, 1)
	assert.True(t, IsClientError(err), "got %v", err)
}

func TestSoftDelete_RefusedWhenPosted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, lineUUID, itemUUID := seedSentPO(t, pool, 100)
	r, err := CreateAndPost(ctx, pool, irInput(poUUID, lineUUID, 10), false, 1)
	require.NoError(t, err)

	err = SoftDelete(ctx, pool, r.ID, 1)
	assert.ErrorIs(t, err, ErrAlreadyPosted)
	onHand, ledger := stockFor(t, pool, itemUUID)
	assert.Equal(t, 10.0, onHand)
	assert.Equal(t, onHand, ledger)
}
