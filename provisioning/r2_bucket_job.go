package provisioning

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"stonesuite-backend/jobqueue"
	"stonesuite-backend/storage"
)

// JobTypeTenantR2Bucket identifies jobs that retry per-tenant R2 bucket
// creation after provisioning could not complete that step.
const JobTypeTenantR2Bucket = "tenant_r2_bucket"

// r2JobTimeout bounds a single R2 bucket retry attempt.
const r2JobTimeout = 2 * time.Minute

// defaultCORSOrigin is used when no CORS origins are configured.
const defaultCORSOrigin = "http://localhost:5173"

// R2BucketJob is the payload of a JobTypeTenantR2Bucket job.
type R2BucketJob struct {
	TenantID string `json:"tenantId"`
	Slug     string `json:"slug"`
}

// R2BucketIdempotencyKey returns the queue idempotency key for a tenant's
// R2 bucket retry job.
func R2BucketIdempotencyKey(tenantID string) string {
	return JobTypeTenantR2Bucket + ":" + tenantID
}

// ensureR2Bucket creates the tenant's bucket (idempotent), applies CORS
// (non-fatal) and records the bucket name on the tenant.
func (p *Provisioner) ensureR2Bucket(ctx context.Context, tenantID, slug string) error {
	bucket := storage.BucketName(slug)
	if err := p.cf.CreateBucket(ctx, bucket); err != nil {
		return fmt.Errorf("create r2 bucket %s: %w", bucket, err)
	}
	origins := p.corsOrigins
	if len(origins) == 0 {
		origins = []string{defaultCORSOrigin}
	}
	if err := p.cf.SetBucketCORS(ctx, bucket, origins); err != nil {
		slog.Warn("provisioning: set cors on r2 bucket failed (non-fatal)", "bucket", bucket, "error", err)
	}
	if err := p.cp.SetTenantR2Bucket(ctx, tenantID, bucket); err != nil {
		return fmt.Errorf("store r2 bucket name: %w", err)
	}
	slog.Info("provisioning: r2 bucket ready", "bucket", bucket, "tenant", slug)
	return nil
}

// enqueueR2BucketRetry queues a retry job after ensureR2Bucket failed; if even
// that fails the tenant falls back to the shared bucket.
func (p *Provisioner) enqueueR2BucketRetry(ctx context.Context, tenantID, slug string, cause error) {
	payload := R2BucketJob{TenantID: tenantID, Slug: slug}
	if _, err := p.queue.Enqueue(ctx, JobTypeTenantR2Bucket, tenantID, payload, R2BucketIdempotencyKey(tenantID)); err != nil {
		slog.Error("provisioning: enqueue r2 bucket retry failed; using shared bucket",
			"tenant", slug, "cause", cause, "error", err)
		return
	}
	slog.Warn("provisioning: r2 bucket creation failed; retry job enqueued", "tenant", slug, "error", cause)
}

// runR2BucketJob executes a claimed R2 bucket retry job. It never touches the
// tenant's migration status.
func (p *Provisioner) runR2BucketJob(ctx context.Context, job *jobqueue.Job) {
	var j R2BucketJob
	if err := json.Unmarshal(job.Payload, &j); err != nil {
		slog.Error("provisioning: bad r2 bucket job payload", "job", job.ID, "error", err)
		if ferr := p.queue.MarkDead(ctx, job.ID, fmt.Sprintf("bad payload: %v", err)); ferr != nil {
			slog.Error("provisioning: mark r2 job dead failed", "job", job.ID, "error", ferr)
		}
		return
	}

	runCtx, cancel := context.WithTimeout(context.Background(), r2JobTimeout)
	defer cancel()

	if err := p.ensureR2Bucket(runCtx, j.TenantID, j.Slug); err != nil {
		slog.Warn("provisioning: r2 bucket retry failed", "tenant", j.Slug, "job", job.ID, "error", err)
		if ferr := p.queue.MarkFailed(ctx, job.ID, err.Error()); ferr != nil {
			slog.Error("provisioning: mark r2 job failed", "job", job.ID, "error", ferr)
		}
		return
	}
	if err := p.queue.MarkSucceeded(ctx, job.ID); err != nil {
		slog.Error("provisioning: mark r2 job succeeded", "job", job.ID, "error", err)
	}
}
