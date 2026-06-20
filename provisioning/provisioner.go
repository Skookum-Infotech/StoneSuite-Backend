package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/authz"
	"stonesuite-backend/database"
	"stonesuite-backend/jobqueue"
	"stonesuite-backend/secret"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// JobTypeTenantProvision identifies tenant-provisioning jobs in the
// durable async_jobs queue.
const JobTypeTenantProvision = "tenant_provision"

// pollInterval is how often idle workers check the queue for new jobs.
const pollInterval = 2 * time.Second

// staleAfter is how long a job may sit in 'running' (e.g. after a worker
// crash) before it's requeued for another attempt.
const staleAfter = 5 * time.Minute

// Job describes a tenant to provision plus the first user (the accepting
// super admin) to seed into the new tenant database.
type Job struct {
	TenantID   string `json:"tenantId"`
	Slug       string `json:"slug"`
	IdentityID string `json:"identityId"`
	Email      string `json:"email"`
	FullName   string `json:"fullName"`
}

// Provisioner creates, migrates, and seeds tenant databases asynchronously.
// Jobs are persisted in the durable async_jobs queue (see jobqueue), so they
// survive restarts and can be claimed by multiple worker instances via
// `SELECT ... FOR UPDATE SKIP LOCKED`.
type Provisioner struct {
	cp       *tenancy.ControlPlane
	provider DBProvider
	cipher   *secret.Cipher // optional; when nil, DSNs are stored in plaintext (dev)
	queue    *jobqueue.Queue

	// cf handles per-tenant R2 bucket creation. nil = no Cloudflare API token
	// configured; bucket step is skipped and tenant falls back to shared bucket.
	cf             *storage.CFClient
	corsOrigins    []string // CORS origins to allow on new tenant buckets

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds a Provisioner. cipher and cf may be nil for local/dev.
func New(cp *tenancy.ControlPlane, provider DBProvider, cipher *secret.Cipher, queue *jobqueue.Queue) *Provisioner {
	return &Provisioner{
		cp:       cp,
		provider: provider,
		cipher:   cipher,
		queue:    queue,
	}
}

// WithCFClient attaches a Cloudflare management API client and the allowed
// CORS origins to use when creating per-tenant R2 buckets. Call before Start.
func (p *Provisioner) WithCFClient(cf *storage.CFClient, corsOrigins []string) *Provisioner {
	p.cf = cf
	p.corsOrigins = corsOrigins
	return p
}

// Start launches the given number of worker goroutines plus a stale-job
// reaper. Workers poll the durable queue; safe to run on multiple instances.
func (p *Provisioner) Start(workers int) {
	if workers < 1 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker(ctx)
	}

	p.wg.Add(1)
	go p.reapStale(ctx)
}

// Enqueue submits a provisioning job. Idempotent per tenant: re-enqueuing
// the same tenant before its job has been claimed is a no-op.
func (p *Provisioner) Enqueue(j Job) {
	idempotencyKey := JobTypeTenantProvision + ":" + j.TenantID
	if _, err := p.queue.Enqueue(context.Background(), JobTypeTenantProvision, j.TenantID, j, idempotencyKey); err != nil {
		log.Printf("provisioning: enqueue tenant %s (%s) failed: %v", j.Slug, j.TenantID, err)
	}
}

// Stop signals all workers to exit and waits for them to finish their
// current poll iteration.
func (p *Provisioner) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

func (p *Provisioner) worker(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.processNext(ctx)
		}
	}
}

// reapStale periodically requeues jobs left 'running' by a crashed worker.
func (p *Provisioner) reapStale(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(staleAfter)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := p.queue.RequeueStale(ctx, staleAfter)
			if err != nil {
				log.Printf("provisioning: requeue stale jobs: %v", err)
			} else if n > 0 {
				log.Printf("provisioning: requeued %d stale job(s)", n)
			}
		}
	}
}

// processNext claims and runs at most one pending provisioning job.
func (p *Provisioner) processNext(ctx context.Context) {
	job, err := p.queue.ClaimNext(ctx, []string{JobTypeTenantProvision})
	if errors.Is(err, jobqueue.ErrNoJob) {
		return
	}
	if err != nil {
		log.Printf("provisioning: claim job: %v", err)
		return
	}

	var j Job
	if err := json.Unmarshal(job.Payload, &j); err != nil {
		log.Printf("provisioning: bad payload for job %s: %v", job.ID, err)
		_ = p.queue.MarkFailed(ctx, job.ID, fmt.Sprintf("bad payload: %v", err))
		return
	}

	runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := p.provision(runCtx, job.ID, j); err != nil {
		log.Printf("provisioning tenant %s (%s) failed: %v", j.Slug, j.TenantID, err)
		_ = p.cp.SetTenantMigrationStatus(context.Background(), j.TenantID, tenancy.MigrationFailed)
		if ferr := p.queue.MarkFailed(ctx, job.ID, err.Error()); ferr != nil {
			log.Printf("provisioning: mark job %s failed: %v", job.ID, ferr)
		}
		return
	}

	log.Printf("provisioning tenant %s (%s) complete", j.Slug, j.TenantID)
	if err := p.queue.MarkSucceeded(ctx, job.ID); err != nil {
		log.Printf("provisioning: mark job %s succeeded: %v", job.ID, err)
	}
}

// provision runs the full pipeline for one tenant. Safe to retry: each step is
// idempotent (create-if-absent, migrations tracked by version, seed upsert).
// jobID is used to record progress so a retried job can be observed mid-flight.
func (p *Provisioner) provision(ctx context.Context, jobID string, j Job) error {
	if err := p.cp.SetTenantStatus(ctx, j.TenantID, tenancy.StatusProvisioning); err != nil {
		return err
	}

	dbName, err := SanitizeDBName(j.Slug)
	if err != nil {
		return err
	}
	_ = p.queue.UpdateProgress(ctx, jobID, map[string]string{"step": "create_database"})
	if err := p.provider.CreateDatabase(ctx, dbName); err != nil {
		return err
	}
	dsn, err := p.provider.DSNFor(dbName)
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	_ = p.queue.UpdateProgress(ctx, jobID, map[string]string{"step": "migrate"})
	version, err := database.ApplyTenantMigrations(ctx, pool)
	if err != nil {
		return err
	}

	// Seed the accepting user as the tenant's first member (idempotent on
	// identity_id). The upsert returns the user id whether inserted or existing.
	_ = p.queue.UpdateProgress(ctx, jobID, map[string]string{"step": "seed_user"})
	var firstUserID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (identity_id, email, full_name, status)
		VALUES ($1, $2, $3, 'active')
		ON CONFLICT (identity_id) DO UPDATE SET email = EXCLUDED.email, updated_at = NOW()
		RETURNING id`,
		j.IdentityID, j.Email, j.FullName).Scan(&firstUserID); err != nil {
		return err
	}

	// Seed RBAC: super_admin system role + grant it to the first user (idempotent).
	_ = p.queue.UpdateProgress(ctx, jobID, map[string]string{"step": "seed_rbac"})
	if err := authz.SeedTenantRBAC(ctx, pool, firstUserID); err != nil {
		return err
	}

	// Seed default workflows (lead/prospect/customer) — idempotent.
	_ = p.queue.UpdateProgress(ctx, jobID, map[string]string{"step": "seed_workflows"})
	if err := workflow.SeedDefaultWorkflows(ctx, pool); err != nil {
		return err
	}

	// Provision a dedicated Cloudflare R2 bucket for this tenant (idempotent).
	// Skip when the Cloudflare client is not configured; the tenant will fall
	// back to the shared global R2_BUCKET at upload time.
	_ = p.queue.UpdateProgress(ctx, jobID, map[string]string{"step": "r2_bucket"})
	if p.cf.IsConfigured() {
		bucket := storage.BucketName(j.Slug)
		if err := p.cf.CreateBucket(ctx, bucket); err != nil {
			log.Printf("provisioning: create r2 bucket for %s: %v (non-fatal, using shared bucket)", j.Slug, err)
		} else {
			origins := p.corsOrigins
			if len(origins) == 0 {
				origins = []string{"http://localhost:5173"}
			}
			if err := p.cf.SetBucketCORS(ctx, bucket, origins); err != nil {
				log.Printf("provisioning: set cors on r2 bucket %s: %v (non-fatal)", bucket, err)
			}
			if err := p.cp.SetTenantR2Bucket(ctx, j.TenantID, bucket); err != nil {
				return fmt.Errorf("store r2 bucket name: %w", err)
			}
			log.Printf("provisioning: r2 bucket %s created for tenant %s", bucket, j.Slug)
		}
	}

	// Store the connection reference (encrypted when a cipher is configured).
	connRef := dsn
	if p.cipher != nil {
		enc, err := p.cipher.Encrypt(dsn)
		if err != nil {
			return err
		}
		connRef = enc
	}
	_ = p.queue.UpdateProgress(ctx, jobID, map[string]string{"step": "done"})
	return p.cp.SetTenantProvisioned(ctx, j.TenantID, dbName, connRef, version)
}
