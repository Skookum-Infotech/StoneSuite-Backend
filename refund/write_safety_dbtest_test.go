//go:build dbtest

package refund

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	in := CreateRefundInput{CustomerUUID: cust, MethodID: firstMethodID(t, pool), Amount: 40}
	in.IdempotencyKey = "rf-replay-" + cust
	in.RequestHash = idempotency.HashRequest([]byte("a"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM refund WHERE refund_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM request_idempotency WHERE idem_key = $1`, in.IdempotencyKey).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	in := CreateRefundInput{CustomerUUID: cust, MethodID: firstMethodID(t, pool), Amount: 40}
	in.IdempotencyKey = "rf-reuse-" + cust
	in.RequestHash = idempotency.HashRequest([]byte("a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_StaleExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	m := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: cust, MethodID: m, Amount: 10}, 1)
	require.NoError(t, err)

	stale := rf.RecordVersion - 1
	_, err = Update(ctx, pool, rf.ID, UpdateRefundInput{MethodID: m, ExpectedVersion: &stale}, 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	current := rf.RecordVersion
	_, err = Update(ctx, pool, rf.ID, UpdateRefundInput{MethodID: m, ExpectedVersion: &current}, 1)
	assert.NoError(t, err)
}

func TestUpdate_VoidedRefundRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	m := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: cust, MethodID: m, Amount: 10}, 1)
	require.NoError(t, err)
	_, err = Transition(ctx, pool, rf.ID, "VOID", 1)
	require.NoError(t, err)

	_, err = Update(ctx, pool, rf.ID, UpdateRefundInput{MethodID: m, Memo: "late"}, 1)
	var ce ClientError
	assert.True(t, errors.As(err, &ce), "expected ClientError, got %v", err)
}
