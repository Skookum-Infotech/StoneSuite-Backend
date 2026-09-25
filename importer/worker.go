package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/Skookum-Infotech/go-rag/parse"
	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/jobqueue"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

const (
	pollInterval = 2 * time.Second
	jobTimeout   = 5 * time.Minute
	// staleAfter must clear jobTimeout by a comfortable margin: runImport's
	// own ctx is bounded to jobTimeout (see processNext), so a job genuinely
	// still 'running' past that has already failed and is on its way to
	// MarkFailed -- staleAfter only needs to catch a worker that crashed
	// outright, mid-run, before it could call MarkFailed itself. 3x leaves
	// room for a slow claim-to-finish handoff without meaningfully delaying
	// real crash recovery.
	staleAfter = 3 * jobTimeout
	// reapInterval is deliberately decoupled from staleAfter: ticking at
	// staleAfter's own period means a job that goes stale just after a tick
	// waits up to another full staleAfter before the next check even looks
	// for it. Checking every minute instead bounds that worst case to
	// staleAfter+1m regardless of when in the cycle a worker crashes.
	reapInterval = 1 * time.Minute
)

// Worker claims and runs import jobs from the shared control-plane job
// queue — its own worker pool, claiming only JobTypeImport, so an import
// backlog can never starve tenant provisioning or vice versa (see
// jobqueue.Queue.ClaimNext's per-type fairness; provisioning.Provisioner is
// the sibling worker this mirrors).
type Worker struct {
	cp     *tenancy.ControlPlane
	router *tenancy.Router
	queue  *jobqueue.Queue
	r2     *storage.Client
	llm    ragcore.LLMClient // optional; nil disables DOCX/PDF field extraction

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewWorker builds a Worker. llm may be nil — a DOCX/PDF import job then
// fails with ErrExtractionUnsupported (recorded on the job, not a panic),
// while CSV/XLSX imports are unaffected since they never need an LLM call.
func NewWorker(cp *tenancy.ControlPlane, router *tenancy.Router, queue *jobqueue.Queue, r2 *storage.Client, llm ragcore.LLMClient) *Worker {
	return &Worker{cp: cp, router: router, queue: queue, r2: r2, llm: llm}
}

// Start launches workers background goroutines plus one stale-job reaper.
func (w *Worker) Start(workers int) {
	if workers < 1 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel

	for i := 0; i < workers; i++ {
		w.wg.Add(1)
		go w.worker(ctx)
	}
	w.wg.Add(1)
	go w.reapStale(ctx)
}

// Stop signals every worker to exit and waits for them to finish their
// current poll iteration.
func (w *Worker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

func (w *Worker) worker(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processNext(ctx)
		}
	}
}

// reapStale periodically requeues jobs left 'running' by a crashed worker.
// Scoped to JobTypeImport — see RequeueStale's doc comment on why an
// unscoped call is a real cross-package race, not just a hypothetical one.
func (w *Worker) reapStale(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := w.queue.RequeueStale(ctx, []string{JobTypeImport}, staleAfter)
			if err != nil {
				log.Printf("importer: requeue stale jobs: %v", err)
			} else if n > 0 {
				log.Printf("importer: requeued %d stale job(s)", n)
			}
		}
	}
}

// processNext claims and runs at most one pending import job.
func (w *Worker) processNext(ctx context.Context) {
	job, err := w.queue.ClaimNext(ctx, []string{JobTypeImport})
	if errors.Is(err, jobqueue.ErrNoJob) {
		return
	}
	if err != nil {
		log.Printf("importer: claim job: %v", err)
		return
	}

	var payload JobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		log.Printf("importer: bad payload for job %s: %v", job.ID, err)
		_ = w.queue.MarkFailed(ctx, job.ID, fmt.Sprintf("bad payload: %v", err))
		return
	}
	if job.TenantID == nil || *job.TenantID == "" {
		log.Printf("importer: job %s has no tenant_id", job.ID)
		_ = w.queue.MarkFailed(ctx, job.ID, "missing tenant_id")
		return
	}

	// Derived from ctx (processNext's own parameter, cancelled by Worker.Stop),
	// not context.Background(): a detached context here would let an in-flight
	// runImport keep going after Stop() asked every worker to exit, with no
	// way to interrupt it short of the process dying outright.
	runCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	if err := w.runImport(runCtx, job.ID, *job.TenantID, payload); err != nil {
		log.Printf("importer: job %s failed: %v", job.ID, err)
		_ = w.queue.MarkFailed(ctx, job.ID, err.Error())
		return
	}
	if err := w.queue.MarkSucceeded(ctx, job.ID); err != nil {
		log.Printf("importer: mark job %s succeeded: %v", job.ID, err)
	}
}

// runImport fetches the uploaded file, parses it, extracts/maps each
// candidate record, and stages every one as an import_rows row for review.
// It never creates a CRM record itself — see CommitJob, run separately once
// a reviewer is satisfied.
func (w *Worker) runImport(ctx context.Context, jobID, tenantID string, payload JobPayload) error {
	tenant, err := w.cp.TenantByID(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("resolve tenant: %w", err)
	}
	pool, err := w.router.PoolFor(ctx, tenant)
	if err != nil {
		return fmt.Errorf("resolve tenant pool: %w", err)
	}

	raw, err := w.r2.WithBucket(tenant.R2Bucket).Get(ctx, payload.StorageKey)
	if err != nil {
		return fmt.Errorf("fetch uploaded file: %w", err)
	}

	p, ok := parse.ForExt(filepath.Ext(payload.FileName))
	if !ok {
		return fmt.Errorf("unsupported file type %q", filepath.Ext(payload.FileName))
	}
	doc, err := parse.Run(ctx, p, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("parse uploaded file: %w", err)
	}

	def, err := LoadWorkflowDefinition(ctx, pool, payload.WorkflowKey)
	if err != nil {
		return fmt.Errorf("load workflow fields: %w", err)
	}

	rows := NewStore(pool)

	// Only after fetch/parse/definition-load all succeed: a job that dies
	// before this point (bad file, unknown workflow) must not wipe out rows a
	// PRIOR successful staging run already produced for it. Clearing here,
	// right before staging begins, is what makes a restage (worker crash,
	// stale-reap requeue, or the queue's own attempts-based retry — anything
	// that runs runImport more than once for the same job_id) converge on
	// exactly this run's rows instead of accumulating a duplicate set
	// alongside whatever a failed prior attempt left behind. See
	// UpsertRow's doc comment for the other half of this contract.
	if err := rows.DeletePendingRowsForJob(ctx, jobID); err != nil {
		return fmt.Errorf("clear prior staged rows: %w", err)
	}

	switch {
	case doc.Rows != nil:
		return w.stageTabular(ctx, rows, jobID, doc.Rows, payload.ColumnMapping, def.Fields)
	case doc.Text != "":
		return w.stageExtracted(ctx, rows, jobID, doc.Text, def.Fields)
	default:
		return fmt.Errorf("parsed document has neither rows nor text")
	}
}

// stageTabular maps and stages one row per parsed table row (CSV/XLSX).
func (w *Worker) stageTabular(ctx context.Context, rows *Store, jobID string, tableRows []map[string]string, mapping map[string]string, defs []workflow.FieldDefinition) error {
	total := len(tableRows)
	_ = w.queue.UpdateProgress(ctx, jobID, map[string]any{"step": "staging", "staged": 0, "total": total})
	for i, tr := range tableRows {
		mapped := ApplyColumnMapping(tr, mapping, defs)
		if _, err := rows.UpsertRow(ctx, jobID, i, tr, mapped, stageErrors(defs, mapped.Custom)); err != nil {
			return fmt.Errorf("stage row %d: %w", i, err)
		}
		_ = w.queue.UpdateProgress(ctx, jobID, map[string]any{"step": "staging", "staged": i + 1, "total": total})
	}
	return nil
}

// stageExtracted runs one LLM extraction over the whole document and stages
// the result as a single candidate record — a free-form document (DOCX/PDF)
// is treated as describing one record, not a table of many.
func (w *Worker) stageExtracted(ctx context.Context, rows *Store, jobID, text string, defs []workflow.FieldDefinition) error {
	_ = w.queue.UpdateProgress(ctx, jobID, map[string]any{"step": "extracting"})
	custom, err := ExtractFields(ctx, w.llm, defs, text)
	if err != nil {
		return fmt.Errorf("extract fields: %w", err)
	}
	mapped := MappedFields{Core: map[string]any{}, Custom: custom}
	if _, err := rows.UpsertRow(ctx, jobID, 0, map[string]string{"text": text}, mapped, stageErrors(defs, custom)); err != nil {
		return fmt.Errorf("stage extracted record: %w", err)
	}
	_ = w.queue.UpdateProgress(ctx, jobID, map[string]any{"step": "staging", "staged": 1, "total": 1})
	return nil
}
