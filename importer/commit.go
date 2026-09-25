package importer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/crmstore"
	"stonesuite-backend/workflow"
)

// LoadWorkflowDefinition resolves a workflow key to its full Definition
// (field definitions + CustomFieldsEnabled) — the same lookup
// controllers/workflow.go uses for the manual create-record path, so an
// import commits through identical field rules. Exported for
// controllers/import.go, which needs it to validate a column mapping and
// build the review UI's field list before a job ever reaches CommitJob.
func LoadWorkflowDefinition(ctx context.Context, pool *pgxpool.Pool, key string) (*workflow.Definition, error) {
	wf, err := workflow.GetWorkflowByKey(ctx, pool, key)
	if err != nil {
		return nil, fmt.Errorf("get workflow %q: %w", key, err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return nil, fmt.Errorf("load definition for %q: %w", key, err)
	}
	def.Workflow = *wf
	return def, nil
}

// Summary tallies the outcome of one CommitJob run.
type Summary struct {
	Committed int `json:"committed"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

// rowUpdater is the subset of *Store CommitJob needs to stage its outcome.
// Defined here, at the point of use, rather than taking *Store directly, so a
// test can substitute a fake that fails partway through a batch — the one
// behavior (a Summary reflecting exactly what happened before an internal
// error, returned alongside that error rather than discarded) that matters
// most for CommitJob's callers to get right, and the one hardest to exercise
// against a real database on demand.
type rowUpdater interface {
	ListByJob(ctx context.Context, jobID string) ([]Row, error)
	MarkFailed(ctx context.Context, id string, errs []string) error
	MarkCommitted(ctx context.Context, id, recordID string) error
}

// CommitJob turns every still-pending (or previously failed) staged row of
// jobID into a real CRM record via the exact same crmstore.Store.CreateRecord
// / workflow.ValidateCustomFields path a manual create-record call uses — see
// the importer package doc. It is safe to call more than once for the same
// job: a row already StatusCommitted or StatusSkipped is counted as skipped,
// never recommitted, and one row's failure never aborts the rest of the
// batch.
//
// If rows.MarkFailed/MarkCommitted itself errors (a genuine write failure,
// not a validation rejection), the loop stops and returns early — but the
// Summary returned alongside that error still reflects every row scored
// before the failure. Callers MUST use it: discarding it because err != nil
// is what let a batch that had already created hundreds of real records
// report back as a flat, misleading "commit failed".
func CommitJob(ctx context.Context, pool *pgxpool.Pool, store crmstore.Store, rows rowUpdater, jobID, workflowKey, actorIdentityID string) (Summary, error) {
	def, err := LoadWorkflowDefinition(ctx, pool, workflowKey)
	if err != nil {
		return Summary{}, err
	}

	staged, err := rows.ListByJob(ctx, jobID)
	if err != nil {
		return Summary{}, fmt.Errorf("list staged rows: %w", err)
	}

	var summary Summary
	for _, row := range staged {
		if row.Status == StatusCommitted || row.Status == StatusSkipped {
			summary.Skipped++
			continue
		}

		if err := workflow.ValidateCustomFields(def.Fields, row.Mapped.Custom, def.Workflow.CustomFieldsEnabled); err != nil {
			if merr := rows.MarkFailed(ctx, row.ID, validationMessages(err)); merr != nil {
				return summary, fmt.Errorf("record row %s failure: %w", row.ID, merr)
			}
			summary.Failed++
			continue
		}

		rec, err := store.CreateRecord(ctx, pool, workflowKey, crmstore.CreateInput{
			ActorIdentityID: actorIdentityID,
			CoreFields:      row.Mapped.Core,
			CustomFields:    row.Mapped.Custom,
		})
		if err != nil {
			if merr := rows.MarkFailed(ctx, row.ID, []string{err.Error()}); merr != nil {
				return summary, fmt.Errorf("record row %s failure: %w", row.ID, merr)
			}
			summary.Failed++
			continue
		}

		if err := rows.MarkCommitted(ctx, row.ID, rec.ID); err != nil {
			return summary, fmt.Errorf("mark row %s committed: %w", row.ID, err)
		}
		summary.Committed++
	}
	return summary, nil
}
