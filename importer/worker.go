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
	staleAfter   = 10 * time.Minute
	jobTimeout   = 5 * time.Minute
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

// reapStale periodically requeues jobs left 'running' by a crashed worker —
// mirrors provisioning.Provisioner.reapStale exactly.
func (w *Worker) reapStale(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(staleAfter)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := w.queue.RequeueStale(ctx, staleAfter)
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

	runCtx, cancel := context.WithTimeout(context.Background(), jobTimeout)
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
		if _, err := rows.InsertRow(ctx, jobID, i, tr, mapped, stageErrors(defs, mapped.Custom)); err != nil {
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
	if _, err := rows.InsertRow(ctx, jobID, 0, map[string]string{"text": text}, mapped, stageErrors(defs, custom)); err != nil {
		return fmt.Errorf("stage extracted record: %w", err)
	}
	_ = w.queue.UpdateProgress(ctx, jobID, map[string]any{"step": "staging", "staged": 1, "total": 1})
	return nil
}
