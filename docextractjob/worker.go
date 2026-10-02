package docextractjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/jobqueue"
	"stonesuite-backend/metrics"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

const (
	pollInterval = 2 * time.Second
	// jobTimeout bounds one extraction: parse is seconds, but the fallback may
	// wait out an Ollama cold start plus its own LLM timeout.
	jobTimeout = 5 * time.Minute
	// staleAfter clears jobTimeout by a margin, so only a worker that crashed
	// mid-run (not one still within its own deadline) gets its job requeued.
	staleAfter   = 3 * jobTimeout
	reapInterval = 1 * time.Minute
)

// controlPlane is the slice of tenancy.ControlPlane the worker uses.
type controlPlane interface {
	TenantByID(ctx context.Context, id string) (*tenancy.Tenant, error)
	ListTenants(ctx context.Context) ([]tenancy.Tenant, error)
}

// poolRouter is the slice of tenancy.Router the worker uses.
type poolRouter interface {
	PoolFor(ctx context.Context, t *tenancy.Tenant) (*pgxpool.Pool, error)
}

// Worker claims doc_extract jobs from the shared control-plane queue with a
// single goroutine (concurrency 1 per instance, so the small Ollama box is
// never fanned out), reaps its own stale jobs, and sweeps expired staging
// rows. It claims only JobTypeDocExtract, so an extraction backlog can never
// starve provisioning or imports (see jobqueue.Queue.ClaimNext).
type Worker struct {
	cp      controlPlane
	router  poolRouter
	queue   *jobqueue.Queue
	stores  func(bucket string) objectStore
	llm     ragcore.LLMClient // optional; nil disables the fallback
	waker   Waker             // optional; nil skips the lazy Ollama wake
	notify  NotifyFunc        // optional
	cfg     Config
	sweepIv time.Duration

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewWorker builds a Worker. llm, waker and notify may be nil (fallback off,
// no wake, no notifications); pass a nil interface, not a typed nil pointer.
func NewWorker(cp controlPlane, router poolRouter, queue *jobqueue.Queue, r2 *storage.Client,
	llm ragcore.LLMClient, waker Waker, notify NotifyFunc, cfg Config) *Worker {
	return &Worker{
		cp: cp, router: router, queue: queue, llm: llm, waker: waker, notify: notify, cfg: cfg,
		stores:  r2Stores(r2),
		sweepIv: sweepInterval,
	}
}

// r2Stores returns the per-tenant-bucket store factory; a missing client or
// bucket yields nil, never a typed-nil interface.
func r2Stores(r2 *storage.Client) func(string) objectStore {
	return func(bucket string) objectStore {
		c := r2.WithBucket(bucket)
		if c == nil {
			return nil
		}
		return c
	}
}

// Start launches the worker, the stale-job reaper and the sweeper. Every
// goroutine exits when Stop cancels the shared context.
func (w *Worker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.wg.Add(3)
	go w.run(ctx)
	go w.reapStale(ctx)
	go w.sweepLoop(ctx)
}

// Stop signals every goroutine to exit and waits for them.
func (w *Worker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

// run polls for jobs until ctx is cancelled.
func (w *Worker) run(ctx context.Context) {
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

// reapStale requeues doc_extract jobs left running by a crashed worker. It is
// scoped to this job type: an unscoped call would requeue another pool's
// legitimately running job (see jobqueue.Queue.RequeueStale).
func (w *Worker) reapStale(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := w.queue.RequeueStale(ctx, []string{JobTypeDocExtract}, staleAfter)
			if err != nil {
				slog.ErrorContext(ctx, "docextract: requeue stale jobs", "error", err)
			} else if n > 0 {
				slog.InfoContext(ctx, "docextract: requeued stale jobs", "count", n)
			}
		}
	}
}

// processNext claims and runs at most one pending job.
func (w *Worker) processNext(ctx context.Context) {
	job, err := w.queue.ClaimNext(ctx, []string{JobTypeDocExtract})
	if errors.Is(err, jobqueue.ErrNoJob) {
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "docextract: claim job", "error", err)
		return
	}
	var payload JobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.ExtractionID == "" {
		w.finishDead(ctx, job.ID, nil, "", "bad payload")
		return
	}
	if job.TenantID == nil || *job.TenantID == "" {
		w.finishDead(ctx, job.ID, nil, payload.ExtractionID, "missing tenant_id")
		return
	}

	// Derived from ctx (cancelled by Stop), never context.Background(): a
	// detached context would let an in-flight job outlive Stop.
	runCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	out, store := w.runJob(runCtx, *job.TenantID, payload.ExtractionID)
	metrics.ObserveDocExtractJob(out.kind, out.method)

	switch out.kind {
	case outcomeSuccess, outcomeSkipped:
		if err := w.queue.MarkSucceeded(ctx, job.ID); err != nil {
			slog.ErrorContext(ctx, "docextract: mark job succeeded", "jobId", job.ID, "error", err)
		}
	case outcomeDead:
		slog.WarnContext(ctx, "docextract: job failed terminally", "jobId", job.ID, "code", out.code, "error", out.err)
		w.finishDead(ctx, job.ID, store, payload.ExtractionID, out.code)
	default:
		slog.WarnContext(ctx, "docextract: job failed transiently", "jobId", job.ID, "attempt", job.Attempts, "error", out.err)
		if job.Attempts >= job.MaxAttempts && store != nil {
			w.markExtractionFailed(ctx, store, payload.ExtractionID, FailTransient)
		}
		if err := w.queue.MarkFailed(ctx, job.ID, fmt.Sprintf("%v", out.err)); err != nil {
			slog.ErrorContext(ctx, "docextract: mark job failed", "jobId", job.ID, "error", err)
		}
	}
}

// runJob resolves the tenant pool and runs the pipeline. It returns the Store
// it used (nil when the tenant could not be resolved) so failures can be
// recorded on the extraction row.
func (w *Worker) runJob(ctx context.Context, tenantID, extractionID string) (out outcome, store *Store) {
	defer func() {
		if x := recover(); x != nil {
			out = dead(FailInternal, fmt.Errorf("worker panic: %v", x))
		}
	}()
	tenant, err := w.cp.TenantByID(ctx, tenantID)
	if err != nil {
		return retry(fmt.Errorf("resolve tenant: %w", err)), nil
	}
	pool, err := w.router.PoolFor(ctx, tenant)
	if err != nil {
		return retry(fmt.Errorf("resolve tenant pool: %w", err)), nil
	}
	store = NewStore(pool)
	r := &jobRunner{
		store: store, pool: pool, objects: w.stores(tenant.R2Bucket), cfg: w.cfg,
		llm: w.llm, waker: w.waker, notify: w.notify, tenantID: tenantID,
	}
	return r.process(ctx, extractionID), store
}

// finishDead records the failure on the extraction (when its store is known)
// and moves the job straight to dead.
func (w *Worker) finishDead(ctx context.Context, jobID string, store *Store, extractionID, code string) {
	if store != nil {
		w.markExtractionFailed(ctx, store, extractionID, code)
	}
	if err := w.queue.MarkDead(ctx, jobID, code); err != nil {
		slog.ErrorContext(ctx, "docextract: mark job dead", "jobId", jobID, "error", err)
	}
}

// markExtractionFailed records the failure code, logging (not returning) errors.
func (w *Worker) markExtractionFailed(ctx context.Context, store *Store, extractionID, code string) {
	if err := store.MarkFailed(ctx, extractionID, code); err != nil {
		slog.ErrorContext(ctx, "docextract: mark extraction failed", "extractionId", extractionID, "error", err)
	}
}
