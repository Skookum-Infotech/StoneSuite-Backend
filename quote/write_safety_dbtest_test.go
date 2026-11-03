//go:build dbtest

package quote

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/idempotency"
	"stonesuite-backend/salesorder"
)

func baseCreateInput(cust, item string) CreateQuoteInput {
	in := CreateQuoteInput{CustomerUUID: cust}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: item, Quantity: 1, UnitPrice: 10}}
	return in
}

func baseUpdateInput(item string) UpdateQuoteInput {
	in := UpdateQuoteInput{}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: item, Quantity: 1, UnitPrice: 10}}
	return in
}

func TestCreate_IdempotencyReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t, pool)
	in := baseCreateInput(cust, item)
	in.IdempotencyKey = "key-replay-" + cust
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
		`SELECT COUNT(*) FROM quote WHERE quote_uuid = $1`, first.ID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreate_IdempotencyKeyReuse(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t, pool)
	in := baseCreateInput(cust, item)
	in.IdempotencyKey = "key-reuse-" + cust
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
	cust, item := seedCustomerAndItem(t, pool)
	q, err := Create(ctx, pool, baseCreateInput(cust, item), 1)
	require.NoError(t, err)

	stale := q.RecordVersion + 99
	upd := baseUpdateInput(item)
	upd.ExpectedVersion = &stale
	_, err = Update(ctx, pool, q.ID, upd, 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	cur := q.RecordVersion
	upd = baseUpdateInput(item)
	upd.Memo = "edited"
	upd.ExpectedVersion = &cur
	after, err := Update(ctx, pool, q.ID, upd, 1)
	require.NoError(t, err)
	assert.Equal(t, "edited", after.Memo)
	assert.Equal(t, q.RecordVersion+1, after.RecordVersion)
}

func TestCreate_RejectsBadInput(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t, pool)

	tooMany := baseCreateInput(cust, item)
	tooMany.Items = make([]LineInput, maxLineItems+1)
	early := baseCreateInput(cust, item)
	early.QuoteDate, early.ValidUntil = "2026-02-01", "2026-01-01"

	tests := []struct {
		name string
		in   CreateQuoteInput
	}{
		{"too many lines", tooMany},
		{"valid until before quote date", early},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Create(ctx, pool, tc.in, 1)
			assert.True(t, IsClientError(err), "got %v", err)
		})
	}
}

func TestUpdate_RefusedOnCancelledQuote(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t, pool)
	q, err := Create(ctx, pool, baseCreateInput(cust, item), 1)
	require.NoError(t, err)
	setStatus(t, pool, q.ID, "CANC")

	_, err = Update(ctx, pool, q.ID, baseUpdateInput(item), 1)
	assert.True(t, IsClientError(err), "got %v", err)
}

func TestSoftDelete_RefusedWhenConverted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cust, item := seedCustomerAndItem(t, pool)

	byStatus, err := Create(ctx, pool, baseCreateInput(cust, item), 1)
	require.NoError(t, err)
	setStatus(t, pool, byStatus.ID, "CONV")
	err = SoftDelete(ctx, pool, byStatus.ID, 1)
	assert.True(t, IsClientError(err), "got %v", err)

	// A live sales order recorded in quote_conversion also blocks the delete.
	// A free-text line keeps the conversion from needing stock.
	linkIn := CreateQuoteInput{CustomerUUID: cust}
	linkIn.Items = []LineInput{{LineNumber: 1, Description: "Install", Quantity: 1, UnitPrice: 10}}
	byLink, err := Create(ctx, pool, linkIn, 1)
	require.NoError(t, err)
	_, created, err := salesorder.ConvertFromQuote(ctx, pool, byLink.ID, 1)
	require.NoError(t, err)
	require.True(t, created)
	err = SoftDelete(ctx, pool, byLink.ID, 1)
	assert.True(t, IsClientError(err), "got %v", err)

	// An unconverted quote still deletes.
	plain, err := Create(ctx, pool, baseCreateInput(cust, item), 1)
	require.NoError(t, err)
	require.NoError(t, SoftDelete(ctx, pool, plain.ID, 1))
	assert.ErrorIs(t, SoftDelete(ctx, pool, plain.ID, 1), ErrNotFound)
}

// setStatus forces a quote to the given lkp_record_status code.
func setStatus(t *testing.T, pool *pgxpool.Pool, uuid, code string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		UPDATE quote SET quote_status = (SELECT record_status_id FROM lkp_record_status WHERE record_status_code = $2 AND record_status_record_type = quote.record_type)
		WHERE quote_uuid = $1`, uuid, code)
	require.NoError(t, err)
}
