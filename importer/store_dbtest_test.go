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
	id, err := s.UpsertRow(ctx, "job-1", 0, map[string]string{"Name": "Acme"}, mapped, []string{"budget: is required"})
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
	_, err := s.UpsertRow(ctx, "job-2", 2, map[string]string{}, empty, nil)
	require.NoError(t, err)
	_, err = s.UpsertRow(ctx, "job-2", 0, map[string]string{}, empty, nil)
	require.NoError(t, err)
	_, err = s.UpsertRow(ctx, "job-2", 1, map[string]string{}, empty, nil)
	require.NoError(t, err)
	// A different job's rows must never leak into job-2's listing.
	_, err = s.UpsertRow(ctx, "job-other", 0, map[string]string{}, empty, nil)
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
	id, err := s.UpsertRow(ctx, "job-3", 0, map[string]string{}, empty, []string{"bad"})
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
	id, err := s.UpsertRow(ctx, "job-4", 0, map[string]string{}, empty, nil)
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
	id, err := s.UpsertRow(ctx, "job-5", 0, map[string]string{}, empty, nil)
	require.NoError(t, err)
	require.NoError(t, s.MarkCommitted(ctx, id, "11111111-1111-1111-1111-111111111111"))

	require.NoError(t, s.MarkSkipped(ctx, id))

	row, found, err := s.GetRow(ctx, id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StatusCommitted, row.Status)
}

// TestStore_UpsertRowConvergesOnSameRowIndex is the regression test for the
// duplicate-staging bug: restaging a job (worker crash, stale-reap requeue,
// or the queue's own attempts-based retry, anything that calls UpsertRow a
// second time for the same job_id+row_index) must update the existing row,
// never insert a second one.
func TestStore_UpsertRowConvergesOnSameRowIndex(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	first := MappedFields{Core: map[string]any{"name": "Acme v1"}, Custom: map[string]any{}}
	id1, err := s.UpsertRow(ctx, "job-restage", 0, map[string]string{}, first, []string{"stale hint"})
	require.NoError(t, err)

	second := MappedFields{Core: map[string]any{"name": "Acme v2"}, Custom: map[string]any{}}
	id2, err := s.UpsertRow(ctx, "job-restage", 0, map[string]string{}, second, nil)
	require.NoError(t, err)
	require.Equal(t, id1, id2, "restaging the same (job_id, row_index) must converge onto the existing row's id, not mint a new one")

	rows, err := s.ListByJob(ctx, "job-restage")
	require.NoError(t, err)
	require.Len(t, rows, 1, "exactly one row must exist per (job_id, row_index) no matter how many times it's staged")
	require.Equal(t, second, rows[0].Mapped, "the second staging pass's content must win")
	require.Empty(t, rows[0].Errors, "the second pass's (empty) error hints must replace the first pass's stale ones")
}

// TestStore_UpsertRowNeverTouchesACommittedRow guards the other half of the
// same contract: a restage happening after a partial commit already
// succeeded on some rows must leave those rows completely alone.
func TestStore_UpsertRowNeverTouchesACommittedRow(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	committed := MappedFields{Core: map[string]any{"name": "Real Record"}, Custom: map[string]any{}}
	id, err := s.UpsertRow(ctx, "job-restage-committed", 0, map[string]string{}, committed, nil)
	require.NoError(t, err)
	require.NoError(t, s.MarkCommitted(ctx, id, "33333333-3333-3333-3333-333333333333"))

	restaged := MappedFields{Core: map[string]any{"name": "Different Content Entirely"}, Custom: map[string]any{}}
	returnedID, err := s.UpsertRow(ctx, "job-restage-committed", 0, map[string]string{}, restaged, nil)
	require.NoError(t, err, "restaging over a committed row must not error")
	require.Empty(t, returnedID, "no id is returned when the conflicting row was protected from update")

	row, found, err := s.GetRow(ctx, id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StatusCommitted, row.Status)
	require.Equal(t, committed, row.Mapped, "a committed row must never be overwritten by a restage, no matter what the new content is")
}

// TestStore_DeletePendingRowsForJob covers the other half of P0-3: shrinkage.
// A retried run that stages fewer rows than a failed prior attempt did must
// not leave the failed attempt's extra tail rows behind -- UpsertRow alone
// only converges indexes both runs happen to reach.
func TestStore_DeletePendingRowsForJob(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	empty := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	pendingID, err := s.UpsertRow(ctx, "job-clear", 0, map[string]string{}, empty, nil)
	require.NoError(t, err)
	failedID, err := s.UpsertRow(ctx, "job-clear", 1, map[string]string{}, empty, nil)
	require.NoError(t, err)
	require.NoError(t, s.MarkFailed(ctx, failedID, []string{"boom"}))
	committedID, err := s.UpsertRow(ctx, "job-clear", 2, map[string]string{}, empty, nil)
	require.NoError(t, err)
	require.NoError(t, s.MarkCommitted(ctx, committedID, "44444444-4444-4444-4444-444444444444"))
	otherJobID, err := s.UpsertRow(ctx, "job-clear-untouched", 0, map[string]string{}, empty, nil)
	require.NoError(t, err)

	require.NoError(t, s.DeletePendingRowsForJob(ctx, "job-clear"))

	_, found, err := s.GetRow(ctx, pendingID)
	require.NoError(t, err)
	require.False(t, found, "a pending row must be cleared")

	_, found, err = s.GetRow(ctx, failedID)
	require.NoError(t, err)
	require.False(t, found, "a failed row must be cleared -- a restage gets a fresh chance at it")

	row, found, err := s.GetRow(ctx, committedID)
	require.NoError(t, err)
	require.True(t, found, "a committed row must survive a clear")
	require.Equal(t, StatusCommitted, row.Status)

	_, found, err = s.GetRow(ctx, otherJobID)
	require.NoError(t, err)
	require.True(t, found, "clearing one job's rows must never touch another job's")
}

func TestStore_MarkCommittedSetsRecordID(t *testing.T) {
	pool := newTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	empty := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	id, err := s.UpsertRow(ctx, "job-6", 0, map[string]string{}, empty, nil)
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
