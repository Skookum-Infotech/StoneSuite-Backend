//go:build dbtest

package importer

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/crmstore"
	"stonesuite-backend/workflow"
)

// commitProbeSeq gives every insertTestWorkflow call in one test binary run
// its own fixed, short key (workflows.key is VARCHAR(64), so this can't
// embed a full subtest name). Following workflow/numbering_assign_dbtest_test.go's
// pattern, each call also deletes any same-keyed row left behind by a
// previous run against a persistent (non-CI-fresh) database before
// inserting, and registers its own cleanup — so this is safe whether the
// database is a throwaway CI instance or a developer's reused local one.
var commitProbeSeq int

func insertTestWorkflow(t *testing.T, pool *pgxpool.Pool, customFieldsEnabled bool) string {
	t.Helper()
	commitProbeSeq++
	key := fmt.Sprintf("commit_probe_%d", commitProbeSeq)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `DELETE FROM workflows WHERE LOWER(key) = LOWER($1)`, key)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO workflows (key, name, description, enabled, is_default, custom_fields_enabled)
		VALUES ($1, 'Commit Probe', '', TRUE, FALSE, $2)`, key, customFieldsEnabled)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workflows WHERE LOWER(key) = LOWER($1)`, key)
	})
	return key
}

// testUUID returns a syntactically valid, distinct uuid for each n — Row's
// record_id column is a real uuid FK-shaped field, so a fake CreateRecord
// can't return an arbitrary string.
func testUUID(n int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
}

// fakeCrmStore is a minimal crmstore.Store double: embedding the (nil)
// interface and overriding only CreateRecord, the one method CommitJob calls
// — the same pattern as crmstore/indexing_store_test.go's fakeStore. Calling
// anything else would panic on the nil embedded interface, which is fine:
// nothing here does.
type fakeCrmStore struct {
	crmstore.Store
	// created counts CreateRecord calls, so a test can assert a row that
	// should never reach it (already committed/skipped, or failed
	// validation first) really didn't.
	created int
	// failOnCall, if non-zero, makes the failOnCall'th CreateRecord call
	// return errCreateRecord instead of succeeding.
	failOnCall int
}

var errCreateRecord = errors.New("simulated CreateRecord failure")

func (f *fakeCrmStore) CreateRecord(_ context.Context, _ *pgxpool.Pool, _ string, _ crmstore.CreateInput) (*workflow.Record, error) {
	f.created++
	if f.failOnCall != 0 && f.created == f.failOnCall {
		return nil, errCreateRecord
	}
	return &workflow.Record{ID: testUUID(f.created)}, nil
}

// failMarkCommittedAfter wraps a real *Store and fails MarkCommitted from the
// (n+1)th call onward, while ListByJob/MarkFailed still hit the real
// database unchanged. This is what lets a test force the exact "an internal
// write failure aborts CommitJob partway through a batch" path deterministically,
// without needing to actually break Postgres.
type failMarkCommittedAfter struct {
	*Store
	n     int
	calls int
}

var errMarkCommitted = errors.New("simulated MarkCommitted failure")

func (f *failMarkCommittedAfter) MarkCommitted(ctx context.Context, id, recordID string) error {
	f.calls++
	if f.calls > f.n {
		return errMarkCommitted
	}
	return f.Store.MarkCommitted(ctx, id, recordID)
}

func TestCommitJob_PartialFailureReturnsSummaryAlongsideError(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	key := insertTestWorkflow(t, pool, false)
	rows := NewStore(pool)

	jobID := "job-commit-partial"
	empty := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	for i := 0; i < 3; i++ {
		_, err := rows.InsertRow(ctx, jobID, i, map[string]string{}, empty, nil)
		require.NoError(t, err)
	}

	crm := &fakeCrmStore{}
	// Real MarkCommitted succeeds for row 0, then fails for every row after —
	// simulating a write failure partway through what CommitJob's own doc
	// comment calls "a genuine write failure, not a validation rejection".
	updater := &failMarkCommittedAfter{Store: rows, n: 1}

	summary, err := CommitJob(ctx, pool, crm, updater, jobID, key, "actor-1")

	require.Error(t, err, "an internal write failure must stop the batch and be reported")
	require.Equal(t, 1, summary.Committed,
		"the Summary returned ALONGSIDE the error must still reflect the one row that "+
			"genuinely committed before the failure -- discarding it is exactly the bug "+
			"this test guards: a caller that only checks err would tell the user the "+
			"whole import failed when a real CRM record was in fact created")
	require.Equal(t, 0, summary.Failed)
	require.Equal(t, 0, summary.Skipped)
	require.Equal(t, 2, crm.created, "CreateRecord runs for row 0 (committed) and row 1 (whose MarkCommitted then fails) before the loop stops -- row 2 is never reached")

	// The row whose MarkCommitted call failed must not be silently left
	// looking "pending" forever with no record link and no error explaining
	// why -- it stays visibly stuck rather than vanishing.
	staged, lerr := rows.ListByJob(ctx, jobID)
	require.NoError(t, lerr)
	require.Equal(t, StatusCommitted, staged[0].Status)
	require.Equal(t, StatusPending, staged[1].Status, "the row CommitJob was updating when the write failed keeps its prior status, not a false 'committed'")
}

func TestCommitJob_AlreadyCommittedOrSkippedRowsAreNeverReprocessed(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	key := insertTestWorkflow(t, pool, false)
	rows := NewStore(pool)

	jobID := "job-commit-idempotent"
	empty := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	committedID, err := rows.InsertRow(ctx, jobID, 0, map[string]string{}, empty, nil)
	require.NoError(t, err)
	require.NoError(t, rows.MarkCommitted(ctx, committedID, testUUID(0)))

	skippedID, err := rows.InsertRow(ctx, jobID, 1, map[string]string{}, empty, nil)
	require.NoError(t, err)
	require.NoError(t, rows.MarkSkipped(ctx, skippedID))

	pendingID, err := rows.InsertRow(ctx, jobID, 2, map[string]string{}, empty, nil)
	require.NoError(t, err)

	crm := &fakeCrmStore{}
	summary, err := CommitJob(ctx, pool, crm, rows, jobID, key, "actor-1")
	require.NoError(t, err)

	require.Equal(t, 1, summary.Committed, "only the still-pending row should commit")
	require.Equal(t, 0, summary.Failed)
	require.Equal(t, 2, summary.Skipped, "the already-committed and already-skipped rows both count as skipped, not recommitted")
	require.Equal(t, 1, crm.created, "CreateRecord must be called exactly once -- re-running Commit on a job must never create a second record for a row that already has one")

	pendingRow, found, err := rows.GetRow(ctx, pendingID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StatusCommitted, pendingRow.Status)
}

func TestCommitJob_ValidationFailureIsSkippedNotFatal(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	key := insertTestWorkflow(t, pool, true)
	rows := NewStore(pool)

	// Seed one required custom field definition directly -- ValidateMapping
	// and the staging worker aren't under test here, only CommitJob's own
	// per-row control flow.
	wf, err := workflow.GetWorkflowByKey(ctx, pool, key)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO workflow_field_definitions (workflow_id, key, label, data_type, required, sort_order)
		VALUES ($1, 'budget', 'Budget', 'number', TRUE, 0)`, wf.ID)
	require.NoError(t, err)

	jobID := "job-commit-validation"
	invalid := MappedFields{Core: map[string]any{}, Custom: map[string]any{}} // missing required "budget"
	valid := MappedFields{Core: map[string]any{}, Custom: map[string]any{"budget": 100.0}}
	_, err = rows.InsertRow(ctx, jobID, 0, map[string]string{}, invalid, nil)
	require.NoError(t, err)
	_, err = rows.InsertRow(ctx, jobID, 1, map[string]string{}, valid, nil)
	require.NoError(t, err)

	crm := &fakeCrmStore{}
	summary, err := CommitJob(ctx, pool, crm, rows, jobID, key, "actor-1")
	require.NoError(t, err, "one row failing validation must not abort the batch")

	require.Equal(t, 1, summary.Committed)
	require.Equal(t, 1, summary.Failed)
	require.Equal(t, 1, crm.created, "CreateRecord must never run for a row that failed field validation")

	staged, lerr := rows.ListByJob(ctx, jobID)
	require.NoError(t, lerr)
	require.Equal(t, StatusFailed, staged[0].Status)
	require.NotEmpty(t, staged[0].Errors)
	require.Equal(t, StatusCommitted, staged[1].Status)
}

// TestCommitJob_CreateRecordFailureIsSkippedNotFatal exercises the OTHER
// per-row failure branch in CommitJob (store.CreateRecord erroring, e.g. a
// real constraint violation on the CRM side) -- a different code path than a
// ValidateCustomFields rejection, and one that shares the same "must not
// abort the batch" contract.
func TestCommitJob_CreateRecordFailureIsSkippedNotFatal(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	key := insertTestWorkflow(t, pool, false)
	rows := NewStore(pool)

	jobID := "job-commit-create-error"
	empty := MappedFields{Core: map[string]any{}, Custom: map[string]any{}}
	_, err := rows.InsertRow(ctx, jobID, 0, map[string]string{}, empty, nil)
	require.NoError(t, err)
	_, err = rows.InsertRow(ctx, jobID, 1, map[string]string{}, empty, nil)
	require.NoError(t, err)

	crm := &fakeCrmStore{failOnCall: 1} // the first CreateRecord call errors, the second succeeds
	summary, err := CommitJob(ctx, pool, crm, rows, jobID, key, "actor-1")
	require.NoError(t, err, "a CreateRecord error is a per-row failure, not a batch-aborting one")

	require.Equal(t, 1, summary.Committed)
	require.Equal(t, 1, summary.Failed)
	require.Equal(t, 2, crm.created, "both rows must reach CreateRecord -- the first row's failure must not skip the second")

	staged, lerr := rows.ListByJob(ctx, jobID)
	require.NoError(t, lerr)
	require.Equal(t, StatusFailed, staged[0].Status)
	require.Equal(t, []string{errCreateRecord.Error()}, staged[0].Errors)
	require.Equal(t, StatusCommitted, staged[1].Status)
}
