//go:build dbtest

package crmstore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/duplicate"
)

func customerCountByName(ctx context.Context, t *testing.T, name string) int {
	t.Helper()
	var n int
	require.NoError(t, testPool(t).QueryRow(ctx,
		`SELECT count(*) FROM customer WHERE customer_name = $1 AND customer_deleted_at IS NULL`, name).Scan(&n))
	return n
}

func TestCreateCustomerTx_Commit(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	name := uniqueName("TxCommit")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	rec, err := CreateCustomerTx(ctx, tx, CreateInput{CoreFields: coreFor(name)})
	require.NoError(t, err)
	assert.NotEmpty(t, rec.RecordNumber)
	require.NoError(t, tx.Commit(ctx))

	assert.Equal(t, 1, customerCountByName(ctx, t, name))
	var hist int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM customer_history h JOIN customer c ON c.customer_id = h.customer_id
		WHERE c.customer_uuid = $1 AND h.action = 'create'`, rec.ID).Scan(&hist))
	assert.Equal(t, 1, hist)
}

func TestCreateCustomerTx_Rollback(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	name := uniqueName("TxRollback")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = CreateCustomerTx(ctx, tx, CreateInput{CoreFields: coreFor(name)})
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	assert.Equal(t, 0, customerCountByName(ctx, t, name))
}

func TestCreateCustomerTx_DuplicateAndMissingEmail(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	name := uniqueName("TxDup")
	createRecord(t, pool, "customer", name)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = CreateCustomerTx(ctx, tx, CreateInput{CoreFields: coreFor(name)})
	_, ok := duplicate.As(err)
	assert.True(t, ok, "want *duplicate.Error, got %v", err)

	_, err = CreateCustomerTx(ctx, tx, CreateInput{CoreFields: map[string]any{"customer_name": uniqueName("NoEmail")}})
	var ce ClientError
	assert.True(t, errors.As(err, &ce), "want ClientError, got %v", err)
}
