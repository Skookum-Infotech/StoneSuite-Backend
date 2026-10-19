// Package automation runs date-triggered background work (e.g. dispatching
// scheduled vendor payments) across every servable tenant. It has no ticker of
// its own: the app scales to zero on Fly, so the clock lives outside the
// process — a Cloudflare Cron Trigger calls POST /api/internal/automation/tick,
// which calls Runner.Tick once per invocation.
package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/companyprofile"
	"stonesuite-backend/tenancy"
)

const (
	// maxConcurrentTenants bounds how many tenant databases one tick touches
	// at once, so a large fleet doesn't open a connection storm against Neon.
	maxConcurrentTenants = 4
	// perTenantTimeout caps one tenant's share of a tick so a stuck database
	// can't hold the whole (synchronous) tick request open.
	perTenantTimeout = 2 * time.Minute
	// tickAdvisoryLockKey guards against two overlapping ticks working the
	// same tenant database at once (arbitrary constant, unique to this lock).
	tickAdvisoryLockKey int64 = 0x5354_4b54_4943_4b31 // "STKTICK1"
)

// Result counts what one job did for one tenant.
type Result struct {
	Transitioned int
	Skipped      int
	// Sent counts outbound notifications delivered; Failed those that errored.
	Sent   int
	Failed int
}

// JobEnv is the per-tenant context a Job runs with. Today is the tenant-local
// calendar date (see TenantToday); Loc is the tenant's resolved location.
type JobEnv struct {
	Tenant *tenancy.Tenant
	Now    time.Time
	Today  time.Time
	Loc    *time.Location
}

// Job is one unit of date-triggered work run against a single tenant database.
type Job interface {
	Name() string
	Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error)
}

// newJobEnv builds the JobEnv for a tenant whose company profile names tzName.
func newJobEnv(t *tenancy.Tenant, now time.Time, tzName string, fallback *time.Location) JobEnv {
	return JobEnv{
		Tenant: t,
		Now:    now,
		Today:  TenantToday(now, tzName, fallback),
		Loc:    TenantLocation(tzName, fallback),
	}
}

// TenantLister lists every tenant known to the control plane.
type TenantLister interface {
	ListTenants(ctx context.Context) ([]tenancy.Tenant, error)
}

// PoolSource resolves a tenant to its isolated database pool.
type PoolSource interface {
	PoolFor(ctx context.Context, t *tenancy.Tenant) (*pgxpool.Pool, error)
}

// Summary is the aggregate outcome of one tick across all tenants.
type Summary struct {
	Tenants      int `json:"tenants"`
	Busy         int `json:"busy"`
	Transitioned int `json:"transitioned"`
	Skipped      int `json:"skipped"`
	Failed       int `json:"failed"`
	Sent         int `json:"sent"`
	FailedSends  int `json:"failed_sends"`
}

// add folds one tenant's job result into the tick summary.
func (s *Summary) add(r Result) {
	s.Transitioned += r.Transitioned
	s.Skipped += r.Skipped
	s.Sent += r.Sent
	s.FailedSends += r.Failed
}

// Runner fans a tick out to every servable tenant and runs each Job there.
type Runner struct {
	tenants  TenantLister
	pools    PoolSource
	jobs     []Job
	fallback *time.Location
}

// NewRunner builds a Runner. fallback is the location used for tenants whose
// company profile has no (or an invalid) timezone.
func NewRunner(tenants TenantLister, pools PoolSource, fallback *time.Location, jobs ...Job) *Runner {
	return &Runner{tenants: tenants, pools: pools, jobs: jobs, fallback: fallback}
}

// Tick runs every job once for every servable tenant and returns the summary.
// One tenant (or one job) failing never stops the others; per-tenant failures
// are logged and counted in Summary.Failed. Only failing to list tenants at
// all is returned as an error.
func (r *Runner) Tick(ctx context.Context, now time.Time) (Summary, error) {
	var sum Summary
	tenants, err := r.tenants.ListTenants(ctx)
	if err != nil {
		return sum, fmt.Errorf("automation tick: %w", err)
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, maxConcurrentTenants)
	)
	for i := range tenants {
		t := tenants[i]
		if !t.Servable() {
			continue
		}
		sum.Tenants++
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res, busy, err := r.runTenant(ctx, &t, now)
			mu.Lock()
			defer mu.Unlock()
			sum.add(res)
			if busy {
				sum.Busy++
			}
			if err != nil {
				sum.Failed++
				slog.Error("automation tick: tenant failed", "tenant", t.Slug, "err", err)
			}
		}()
	}
	wg.Wait()
	return sum, nil
}

// runTenant runs every job against one tenant under the per-tenant advisory
// lock. busy reports that another tick already held the lock, in which case
// nothing ran.
func (r *Runner) runTenant(ctx context.Context, t *tenancy.Tenant, now time.Time) (res Result, busy bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, perTenantTimeout)
	defer cancel()

	pool, err := r.pools.PoolFor(ctx, t)
	if err != nil {
		return res, false, fmt.Errorf("tenant pool: %w", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return res, false, fmt.Errorf("acquire lock conn: %w", err)
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, tickAdvisoryLockKey).Scan(&locked); err != nil {
		return res, false, fmt.Errorf("try advisory lock: %w", err)
	}
	if !locked {
		return res, true, nil
	}
	defer func() {
		// Background ctx: the unlock must run even if the tenant ctx timed out,
		// or the session-level lock would ride back into the pool still held.
		if _, uerr := conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, tickAdvisoryLockKey); uerr != nil {
			err = errors.Join(err, fmt.Errorf("advisory unlock: %w", uerr))
		}
	}()

	profile, err := companyprofile.Get(ctx, pool)
	if err != nil {
		return res, false, fmt.Errorf("load company profile: %w", err)
	}
	env := newJobEnv(t, now, profile.Timezone, r.fallback)

	var errs error
	for _, j := range r.jobs {
		jr, jerr := j.Run(ctx, pool, env)
		res.Transitioned += jr.Transitioned
		res.Skipped += jr.Skipped
		res.Sent += jr.Sent
		res.Failed += jr.Failed
		if jerr != nil {
			errs = errors.Join(errs, fmt.Errorf("%s: %w", j.Name(), jerr))
		}
		if jr.Transitioned > 0 || jr.Skipped > 0 || jr.Sent > 0 || jr.Failed > 0 {
			slog.Info("automation job ran", "tenant", t.Slug, "job", j.Name(), "today", env.Today.Format("2006-01-02"),
				"transitioned", jr.Transitioned, "skipped", jr.Skipped, "sent", jr.Sent, "failed_sends", jr.Failed)
		}
	}
	return res, false, errs
}
