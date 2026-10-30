//go:build dbtest

package creditmemo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	in := CreateCreditMemoInput{CustomerUUID: cust, Amount: 40,
		IdempotencyKey: "cm-replay-" + cust, RequestHash: idempotency.HashRequest([]byte("a"))}

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM credit_memo WHERE credit_memo_customer_id = (SELECT customer_id FROM customer WHERE customer_uuid = $1)`, cust).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	in := CreateCreditMemoInput{CustomerUUID: cust, Amount: 40,
		IdempotencyKey: "cm-reuse-" + cust, RequestHash: idempotency.HashRequest([]byte("a"))}
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	in.RequestHash = idempotency.HashRequest([]byte("b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_StaleExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cm, err := Create(ctx, pool, CreateCreditMemoInput{CustomerUUID: seedCustomer(t, pool), Amount: 40}, 1)
	require.NoError(t, err)
	stale := cm.RecordVersion - 1
	_, err = Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Memo: "x", ExpectedVersion: &stale}, 1)
	assert.ErrorIs(t, err, ErrVersionConflict)
	cur := cm.RecordVersion
	_, err = Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Memo: "x", ExpectedVersion: &cur}, 1)
	assert.NoError(t, err)
}

func TestUpdate_IssuedMemoMoneyEditRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cm := seedApprovedMemo(t, pool, seedCustomer(t, pool), 100)
	amt := 5.0
	_, err := Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Amount: &amt}, 1)
	var ce ClientError
	require.ErrorAs(t, err, &ce)

	// Fully applied (terminal) memos can't be edited at all.
	_, err = pool.Exec(ctx, `UPDATE credit_memo SET credit_memo_status =
		(SELECT record_status_id FROM lkp_record_status WHERE record_status_code = 'VOID'
		 AND record_status_record_type = (SELECT record_type_id FROM lkp_record_type WHERE record_type_code='CRDT'))
		WHERE credit_memo_uuid = $1`, cm.ID)
	require.NoError(t, err)
	_, err = Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Memo: "y"}, 1)
	require.ErrorAs(t, err, &ce)
}

func TestApply_IdempotencyKeyAppliesOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	inv := seedSentInvoice(t, pool, cust, 100)
	cm := seedApprovedMemo(t, pool, cust, 100)
	hash := idempotency.HashRequest([]byte("apply"))
	key := "cm-apply-" + cust

	first, err := ApplyIdempotent(ctx, pool, cm.ID, inv, 30, 1, key, hash)
	require.NoError(t, err)
	second, err := ApplyIdempotent(ctx, pool, cm.ID, inv, 30, 1, key, hash)
	require.NoError(t, err)
	assert.Equal(t, first.AppliedTotal, second.AppliedTotal)
	assert.InDelta(t, 30, second.AppliedTotal, 0.001)

	_, err = ApplyIdempotent(ctx, pool, cm.ID, inv, 30, 1, key, idempotency.HashRequest([]byte("other")))
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)

	// No key: unchanged behavior, adds again.
	third, err := Apply(ctx, pool, cm.ID, inv, 30, 1)
	require.NoError(t, err)
	assert.InDelta(t, 60, third.AppliedTotal, 0.001)
}
