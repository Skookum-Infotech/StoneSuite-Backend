//go:build dbtest

package expense

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func oneLine(amount float64) []LineInput {
	return []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: amount}}
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := CreateExpenseInput{expenseFields: expenseFields{Items: oneLine(10)}}
	in.IdempotencyKey = "exp-key-replay-" + t.Name()
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
		`SELECT COUNT(*) FROM expense WHERE expense_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	in := CreateExpenseInput{expenseFields: expenseFields{Items: oneLine(10)}}
	in.IdempotencyKey = "exp-key-reuse-" + t.Name()
	in.RequestHash = idempotency.HashRequest([]byte("body-a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("body-b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestCreate_RejectsBadAmountAndDate(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	for _, amount := range []float64{math.NaN(), 1e14, -5} {
		_, err := Create(ctx, pool, CreateExpenseInput{expenseFields: expenseFields{Items: oneLine(amount)}}, 1)
		assert.True(t, IsClientError(err), "amount %v: got %v", amount, err)
	}
	bad := oneLine(10)
	bad[0].ExpenseDate = "2026-13-45"
	_, err := Create(ctx, pool, CreateExpenseInput{expenseFields: expenseFields{Items: bad}}, 1)
	assert.True(t, IsClientError(err), "got %v", err)
}

func TestUpdate_ExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields: expenseFields{Items: oneLine(10)}}, 1)
	require.NoError(t, err)
	require.Equal(t, 1, created.RecordVersion)

	stale := created.RecordVersion + 5
	_, err = Update(ctx, pool, created.ID, UpdateExpenseInput{
		expenseFields:   expenseFields{Items: oneLine(20)},
		ExpectedVersion: &stale,
	}, 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	current := created.RecordVersion
	updated, err := Update(ctx, pool, created.ID, UpdateExpenseInput{
		expenseFields:   expenseFields{Items: oneLine(20)},
		ExpectedVersion: &current,
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, created.RecordVersion+1, updated.RecordVersion)
	assert.Equal(t, 20.0, updated.Total)

	// A stale version must win over the status gate once the claim leaves DRFT.
	_, err = Transition(ctx, pool, created.ID, "SUBM", 1)
	require.NoError(t, err)
	_, err = Update(ctx, pool, created.ID, UpdateExpenseInput{
		expenseFields:   expenseFields{Items: oneLine(30)},
		ExpectedVersion: &current,
	}, 1)
	assert.ErrorIs(t, err, ErrVersionConflict)
}

func TestSoftDelete_NotFoundAfterDelete(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields: expenseFields{Items: oneLine(10)}}, 1)
	require.NoError(t, err)
	require.NoError(t, SoftDelete(ctx, pool, created.ID, 1))
	assert.ErrorIs(t, SoftDelete(ctx, pool, created.ID, 1), ErrNotFound)
}
