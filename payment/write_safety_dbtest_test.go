//go:build dbtest

package payment

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
)

func firstCurrencyID(t *testing.T) int {
	t.Helper()
	var id int
	if err := testPool(t).QueryRow(context.Background(),
		`SELECT currency_id FROM lkp_currency ORDER BY currency_id LIMIT 1`).Scan(&id); err != nil {
		t.Skipf("no currency seeded: %v", err)
	}
	return id
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	in := CreatePaymentInput{CustomerUUID: cust, MethodID: firstMethodID(t, pool), Amount: 40}
	in.IdempotencyKey = "pay-replay-" + cust
	in.RequestHash = idempotency.HashRequest([]byte("a"))

	first, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	second, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM payment WHERE payment_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM request_idempotency WHERE idem_key = $1`, in.IdempotencyKey).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	in := CreatePaymentInput{CustomerUUID: cust, MethodID: firstMethodID(t, pool), Amount: 40}
	in.IdempotencyKey = "pay-reuse-" + cust
	in.RequestHash = idempotency.HashRequest([]byte("a"))
	_, err := Create(ctx, pool, in, 1)
	require.NoError(t, err)

	in.RequestHash = idempotency.HashRequest([]byte("b"))
	_, err = Create(ctx, pool, in, 1)
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestCreate_FailingInlineApplicationLeavesNoPayment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, inv := seedSentInvoice(t, pool, 50)
	ref := "atomic-" + cust

	// 80 against a 50 balance: the application is rejected after the header
	// insert, so the whole create must roll back.
	_, err := Create(ctx, pool, CreatePaymentInput{
		CustomerUUID: cust, MethodID: firstMethodID(t, pool), Amount: 100, ReferenceNumber: ref,
		Applications: []ApplicationInput{{InvoiceUUID: inv, Amount: 80}},
	}, 1)
	var ce ClientError
	require.ErrorAs(t, err, &ce)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM payment WHERE payment_reference_number = $1`, ref).Scan(&n))
	assert.Equal(t, 0, n)
}

func TestApply_SameKeyAppliesOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, inv := seedSentInvoice(t, pool, 100)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: cust, MethodID: firstMethodID(t, pool), Amount: 100}, 1)
	require.NoError(t, err)
	key, hash := "apply-"+p.ID, idempotency.HashRequest([]byte("x"))

	first, err := ApplyIdempotent(ctx, pool, p.ID, inv, 30, 1, key, hash)
	require.NoError(t, err)
	second, err := ApplyIdempotent(ctx, pool, p.ID, inv, 30, 1, key, hash)
	require.NoError(t, err)
	assert.Equal(t, 30.0, first.AppliedTotal)
	assert.Equal(t, 30.0, second.AppliedTotal)
	require.Len(t, second.Applications, 1)
	assert.Equal(t, 30.0, second.Applications[0].Amount)

	_, err = ApplyIdempotent(ctx, pool, p.ID, inv, 10, 1, key, idempotency.HashRequest([]byte("y")))
	assert.ErrorIs(t, err, idempotency.ErrKeyReuse)
}

func TestUpdate_StaleExpectedVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	m := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: cust, MethodID: m, Amount: 10}, 1)
	require.NoError(t, err)

	stale := p.RecordVersion - 1
	_, err = Update(ctx, pool, p.ID, UpdatePaymentInput{MethodID: m, ExpectedVersion: &stale}, 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	current := p.RecordVersion
	_, err = Update(ctx, pool, p.ID, UpdatePaymentInput{MethodID: m, ExpectedVersion: &current}, 1)
	assert.NoError(t, err)
}

func TestUpdate_VoidedPaymentRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust := seedCustomer(t, pool)
	m := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: cust, MethodID: m, Amount: 10}, 1)
	require.NoError(t, err)
	_, err = Transition(ctx, pool, p.ID, "VOID", 1)
	require.NoError(t, err)

	_, err = Update(ctx, pool, p.ID, UpdatePaymentInput{MethodID: m, Memo: "late"}, 1)
	var ce ClientError
	assert.True(t, errors.As(err, &ce), "expected ClientError, got %v", err)
}

func TestUpdate_CurrencyChangeWithApplicationsRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cur := firstCurrencyID(t)
	cust, inv := seedSentInvoice(t, pool, 100)
	m := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: cust, MethodID: m, Amount: 100}, 1)
	require.NoError(t, err)
	_, err = Apply(ctx, pool, p.ID, inv, 20, 1)
	require.NoError(t, err)

	_, err = Update(ctx, pool, p.ID, UpdatePaymentInput{MethodID: m, CurrencyID: &cur}, 1)
	var ce ClientError
	assert.True(t, errors.As(err, &ce), "expected ClientError, got %v", err)

	// Keeping the currency (unset) is not a change.
	_, err = Update(ctx, pool, p.ID, UpdatePaymentInput{MethodID: m, Memo: "ok"}, 1)
	assert.NoError(t, err)
}
