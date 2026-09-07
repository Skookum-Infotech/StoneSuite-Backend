//go:build dbtest

package importer

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE import_rows`); err != nil {
		t.Fatalf("truncate import_rows: %v", err)
	}
	return pool
}

func TestStore_InsertAndGetRow(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	mapped := MappedFields{Core: map[string]any{"name": "Acme"}, Custom: map[string]any{"budget": "5000"}}
	id, err := s.InsertRow(ctx, "job-1", 0, map[string]string{"Name": "Acme"}, mapped, []string{"budget: is required"})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	row, found, err := s.GetRow(ctx, id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "job-1", row.JobID)
	require.Equal(t, 0, row.RowIndex)
	require.Equal(t, StatusPending, row.Status)
	require.Equal(t, mapped, row.Mapped)
	require.Equal(t, []string{"budget: is required"}, row.Errors)
	require.Empty(t, row.RecordID)
}

func TestStore_GetRowMissingReturnsFoundFalse(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)

	_, found, err := s.GetRow(context.Background(), "00000000-0000-0000-0000-000000000000")
	require.NoError(t, err)
	require.False(t, found)
}

func TestStore_ListByJobOrdersByRowIndex(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	empty := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	_, err := s.InsertRow(ctx, "job-2", 2, map[string]string{}, empty, nil)
	require.NoError(t, err)
	_, err = s.InsertRow(ctx, "job-2", 0, map[string]string{}, empty, nil)
	require.NoError(t, err)
	_, err = s.InsertRow(ctx, "job-2", 1, map[string]string{}, empty, nil)
	require.NoError(t, err)
	// A different job's rows must never leak into job-2's listing.
	_, err = s.InsertRow(ctx, "job-other", 0, map[string]string{}, empty, nil)
	require.NoError(t, err)

	rows, err := s.ListByJob(ctx, "job-2")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, []int{0, 1, 2}, []int{rows[0].RowIndex, rows[1].RowIndex, rows[2].RowIndex})
}

func TestStore_UpdateMappedResetsToPending(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	empty := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	id, err := s.InsertRow(ctx, "job-3", 0, map[string]string{}, empty, []string{"bad"})
	require.NoError(t, err)
	require.NoError(t, s.MarkFailed(ctx, id, []string{"still bad"}))

	corrected := MappedFields{Core: map[string]any{}, Custom: map[string]any{"budget": "9000"}}
	require.NoError(t, s.UpdateMapped(ctx, id, corrected))

	row, found, err := s.GetRow(ctx, id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StatusPending, row.Status)
	require.Equal(t, corrected, row.Mapped)
	require.Empty(t, row.Errors)
}

func TestStore_UpdateMappedNoOpOnCommittedRow(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	empty := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	id, err := s.InsertRow(ctx, "job-4", 0, map[string]string{}, empty, nil)
	require.NoError(t, err)
	require.NoError(t, s.MarkCommitted(ctx, id, "11111111-1111-1111-1111-111111111111"))

	changed := MappedFields{Core: map[string]any{}, Custom: map[string]any{"budget": "1"}}
	require.NoError(t, s.UpdateMapped(ctx, id, changed))

	row, found, err := s.GetRow(ctx, id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StatusCommitted, row.Status, "a committed row's outcome must be final")
	require.Equal(t, empty, row.Mapped, "UpdateMapped must not touch an already-committed row")
}

func TestStore_MarkSkippedNoOpOnCommittedRow(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	empty := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	id, err := s.InsertRow(ctx, "job-5", 0, map[string]string{}, empty, nil)
	require.NoError(t, err)
	require.NoError(t, s.MarkCommitted(ctx, id, "11111111-1111-1111-1111-111111111111"))

	require.NoError(t, s.MarkSkipped(ctx, id))

	row, found, err := s.GetRow(ctx, id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StatusCommitted, row.Status)
}

func TestStore_MarkCommittedSetsRecordID(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	empty := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	id, err := s.InsertRow(ctx, "job-6", 0, map[string]string{}, empty, nil)
	require.NoError(t, err)

	const recordID = "22222222-2222-2222-2222-222222222222"
	require.NoError(t, s.MarkCommitted(ctx, id, recordID))

	row, found, err := s.GetRow(ctx, id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StatusCommitted, row.Status)
	require.Equal(t, recordID, row.RecordID)
	require.Empty(t, row.Errors)
}
