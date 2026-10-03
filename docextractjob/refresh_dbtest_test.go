//go:build dbtest

package docextractjob

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract"
)

// storedResult decodes the persisted result of extraction id.
func storedResult(t *testing.T, s *Store, id string) ResultDoc {
	t.Helper()
	ex, err := s.GetInternal(context.Background(), id)
	require.NoError(t, err)
	var doc ResultDoc
	require.NoError(t, json.Unmarshal(ex.Result, &doc))
	return doc
}

func TestRefresh_SeesDataAddedSinceExtraction(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	name := "Newco" + uniq()

	res := feedbackResult("", "", "")
	res.Extracted.Header.CustomerName = docextract.Field{Value: name}
	e := storeReadyExtraction(t, s, res)

	// After extraction: the customer is created and a clerk saves an order
	// with the same PO number.
	cust := seedCustomer(t, pool, name, "CACT")
	so := seedSalesOrder(t, pool, cust, "SO-R1", "PO-1", "OPEN", 1000)

	ex, err := s.GetInternal(ctx, e.ID)
	require.NoError(t, err)
	fresh, err := Refresh(ctx, pool, ex, storedResult(t, s, e.ID))
	require.NoError(t, err)
	assert.Equal(t, cust, fresh.Resolution.Customer.UUID)
	var dupUUIDs []string
	for _, d := range fresh.Duplicates {
		dupUUIDs = append(dupUUIDs, d.RecordUUID)
	}
	assert.Contains(t, dupUUIDs, so)

	// Persisted, so /complete's learning diff compares against what was shown.
	assert.Equal(t, cust, storedResult(t, s, e.ID).Resolution.Customer.UUID)

	// Nothing changed since: no write.
	ex, err = s.GetInternal(ctx, e.ID)
	require.NoError(t, err)
	again, err := Refresh(ctx, pool, ex, storedResult(t, s, e.ID))
	require.NoError(t, err)
	assert.Equal(t, cust, again.Resolution.Customer.UUID)
	ex2, err := s.GetInternal(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, ex.UpdatedAt, ex2.UpdatedAt)
}

func TestRefresh_LeavesUsedExtractionAlone(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	name := "Usedco" + uniq()

	res := feedbackResult("", "", "")
	res.Extracted.Header.CustomerName = docextract.Field{Value: name}
	e := storeReadyExtraction(t, s, res)
	_, err := pool.Exec(ctx, `UPDATE document_extractions SET status = $2 WHERE id = $1::uuid`, e.ID, StatusUsed)
	require.NoError(t, err)
	seedCustomer(t, pool, name, "CACT")

	ex, err := s.GetInternal(ctx, e.ID)
	require.NoError(t, err)
	got, err := Refresh(ctx, pool, ex, storedResult(t, s, e.ID))
	require.NoError(t, err)
	assert.Empty(t, got.Resolution.Customer.UUID)
	assert.Empty(t, storedResult(t, s, e.ID).Resolution.Customer.UUID)
}
