package provisioning

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/authz"
	"stonesuite-backend/database"
	"stonesuite-backend/secret"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// Job describes a tenant to provision plus the first user (the accepting
// super admin) to seed into the new tenant database.
type Job struct {
	TenantID   string
	Slug       string
	IdentityID string
	Email      string
	FullName   string
}

// Provisioner creates, migrates, and seeds tenant databases asynchronously.
// Work is queued and handled by a worker pool with an explicit shutdown path.
type Provisioner struct {
	cp       *tenancy.ControlPlane
	provider DBProvider
	cipher   *secret.Cipher // optional; when nil, DSNs are stored in plaintext (dev)

	jobs chan Job
	wg   sync.WaitGroup
}

// New builds a Provisioner. cipher may be nil for local/dev.
func New(cp *tenancy.ControlPlane, provider DBProvider, cipher *secret.Cipher) *Provisioner {
	return &Provisioner{
		cp:       cp,
		provider: provider,
		cipher:   cipher,
		jobs:     make(chan Job, 64),
	}
}

// Start launches the given number of worker goroutines.
func (p *Provisioner) Start(workers int) {
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
}

// Enqueue submits a provisioning job (non-blocking unless the buffer is full).
func (p *Provisioner) Enqueue(j Job) { p.jobs <- j }

// Stop drains the queue and waits for workers to exit. Call on shutdown.
func (p *Provisioner) Stop() {
	close(p.jobs)
	p.wg.Wait()
}

func (p *Provisioner) worker() {
	defer p.wg.Done()
	for j := range p.jobs {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := p.provision(ctx, j); err != nil {
			log.Printf("provisioning tenant %s (%s) failed: %v", j.Slug, j.TenantID, err)
			// Best-effort: mark migration failed so the resolver refuses to serve it.
			_ = p.cp.SetTenantMigrationStatus(context.Background(), j.TenantID, tenancy.MigrationFailed)
		} else {
			log.Printf("provisioning tenant %s (%s) complete", j.Slug, j.TenantID)
		}
		cancel()
	}
}

// provision runs the full pipeline for one tenant. Safe to retry: each step is
// idempotent (create-if-absent, migrations tracked by version, seed upsert).
func (p *Provisioner) provision(ctx context.Context, j Job) error {
	if err := p.cp.SetTenantStatus(ctx, j.TenantID, tenancy.StatusProvisioning); err != nil {
		return err
	}

	dbName, err := SanitizeDBName(j.Slug)
	if err != nil {
		return err
	}
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

	version, err := database.ApplyTenantMigrations(ctx, pool)
	if err != nil {
		return err
	}

	// Seed the accepting user as the tenant's first member (idempotent on
	// identity_id). The upsert returns the user id whether inserted or existing.
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
	if err := authz.SeedTenantRBAC(ctx, pool, firstUserID); err != nil {
		return err
	}

	// Seed default workflows (lead/prospect/customer) — idempotent.
	if err := workflow.SeedDefaultWorkflows(ctx, pool); err != nil {
		return err
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
	return p.cp.SetTenantProvisioned(ctx, j.TenantID, dbName, connRef, version)
}
