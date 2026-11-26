package usage

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/safego"
	"stonesuite-backend/tenancy"
)

// Rollup tuning.
const (
	// rollupConcurrency bounds how many tenant databases one rollup touches at once.
	rollupConcurrency = 5
	// rollupTenantTimeout caps one tenant's share of a pass.
	rollupTenantTimeout = 2 * time.Minute
	// rollupSeriesDays is the history window handed to the watch-list rules.
	rollupSeriesDays = 35
	// rollupQuietDays is how far back an alert must have last fired to be resolved (3 quiet days).
	rollupQuietDays = 2
	msPerHour       = 3_600_000.0
	hoursPerDay     = 24
)

// TenantLister lists every tenant known to the control plane.
type TenantLister interface {
	ListTenants(ctx context.Context) ([]tenancy.Tenant, error)
}

// PoolSource resolves a tenant to its isolated database pool.
type PoolSource interface {
	PoolFor(ctx context.Context, t *tenancy.Tenant) (*pgxpool.Pool, error)
}

// rollupStore is the subset of FleetStore the rollup uses.
type rollupStore interface {
	HourlyTotals(ctx context.Context, tenantID string, from, to time.Time) (HourlyTotals, error)
	JobStats(ctx context.Context, tenantID string, from, to time.Time) (JobStats, error)
	SignInStats(ctx context.Context, tenantID string, from, to time.Time) (SignInStats, error)
	UpsertDailyMetrics(ctx context.Context, tenantID string, day time.Time, values map[string]float64) error
	ReplaceTableSizes(ctx context.Context, tenantID string, measuredAt time.Time, sizes []TableSize) error
	DailyValues(ctx context.Context, tenantIDs []string, from, to time.Time) ([]DailyValue, error)
	TokensByDay(ctx context.Context, tenantIDs []string, from, to time.Time) ([]DailyValue, error)
	RecordFiring(ctx context.Context, f AlertFiring, now time.Time) error
	ResolveQuiet(ctx context.Context, tenantID string, quietSince, now time.Time) (int64, error)
	PruneRetention(ctx context.Context, now time.Time) error
}

// r2Measurer measures a tenant's bucket (*StorageRefresher in production).
type r2Measurer interface {
	Measure(ctx context.Context, tenantID, bucket string) (*StorageSnapshot, error)
}

// RollupSummary is the outcome of one rollup run.
type RollupSummary struct {
	Tenants int `json:"tenants"`
	Failed  int `json:"failed"`
}

// tenantRollup is one tenant's pass-1 output, kept for the fleet and rule passes.
type tenantRollup struct {
	tenant tenancy.Tenant
	values map[string]float64
	signIn SignInStats
}

// Rollup computes the daily per-tenant usage metrics and evaluates the watch-list rules.
type Rollup struct {
	tenants   TenantLister
	pools     PoolSource
	store     rollupStore
	r2        r2Measurer
	now       func() time.Time
	newSource func(*pgxpool.Pool) tenantSource
}

// NewRollup builds a Rollup; r2 may be nil when R2 is not configured.
func NewRollup(tenants TenantLister, pools PoolSource, store rollupStore, r2 r2Measurer, now func() time.Time) *Rollup {
	return &Rollup{tenants: tenants, pools: pools, store: store, r2: r2, now: now, newSource: newPGTenantSource}
}

// Run rolls up the UTC day (any time within it), evaluates the rules and prunes old rows.
// One tenant failing never stops the others; only listing tenants or pruning fail the run.
func (r *Rollup) Run(ctx context.Context, day time.Time) (RollupSummary, error) {
	var sum RollupSummary
	d := day.UTC()
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	all, err := r.tenants.ListTenants(ctx)
	if err != nil {
		return sum, fmt.Errorf("usage rollup: list tenants: %w", err)
	}
	servable := make([]tenancy.Tenant, 0, len(all))
	for i := range all {
		if all[i].Servable() {
			servable = append(servable, all[i])
		}
	}
	sum.Tenants = len(servable)

	var mu sync.Mutex
	results := map[string]tenantRollup{}
	failed := r.forEachTenant(ctx, servable, "usage.rollupTenant", func(tctx context.Context, t *tenancy.Tenant) error {
		res, err := r.collect(tctx, t, d)
		if err != nil {
			return err
		}
		mu.Lock()
		results[t.ID] = res
		mu.Unlock()
		return nil
	})

	fleetP95 := r.fleetP95(results)
	var ok []tenancy.Tenant
	for _, t := range servable {
		if _, done := results[t.ID]; done {
			ok = append(ok, t)
		}
	}
	for id := range r.forEachTenant(ctx, ok, "usage.evaluateTenant", func(tctx context.Context, t *tenancy.Tenant) error {
		return r.evaluate(tctx, results[t.ID], d, fleetP95)
	}) {
		failed[id] = true
	}
	sum.Failed = len(failed)

	if err := r.store.PruneRetention(ctx, r.now()); err != nil {
		return sum, fmt.Errorf("usage rollup: %w", err)
	}
	return sum, nil
}

// forEachTenant runs fn per tenant with bounded concurrency, a per-tenant timeout and panic isolation.
// It returns the ids of tenants whose fn failed; failures are logged, never propagated.
func (r *Rollup) forEachTenant(ctx context.Context, tenants []tenancy.Tenant, name string,
	fn func(ctx context.Context, t *tenancy.Tenant) error) map[string]bool {
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		sem    = make(chan struct{}, rollupConcurrency)
		failed = map[string]bool{}
	)
	for i := range tenants {
		t := tenants[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			var err error
			if panicErr := safego.Do(ctx, name, func() {
				tctx, cancel := context.WithTimeout(ctx, rollupTenantTimeout)
				defer cancel()
				err = fn(tctx, &t)
			}); panicErr != nil {
				err = panicErr
			}
			if err != nil {
				mu.Lock()
				failed[t.ID] = true
				mu.Unlock()
				slog.ErrorContext(ctx, "usage rollup: tenant failed", "pass", name, "tenant", t.Slug, "err", err)
			}
		}()
	}
	wg.Wait()
	return failed
}

// collect is pass 1 for one tenant: gather every metric for the day and persist it.
func (r *Rollup) collect(ctx context.Context, t *tenancy.Tenant, day time.Time) (tenantRollup, error) {
	from, to := day, day.AddDate(0, 0, 1)
	values := map[string]float64{}

	hourly, err := r.store.HourlyTotals(ctx, t.ID, from, to)
	if err != nil {
		return tenantRollup{}, err
	}
	jobs, err := r.store.JobStats(ctx, t.ID, from, to)
	if err != nil {
		return tenantRollup{}, err
	}
	signIn, err := r.store.SignInStats(ctx, t.ID, from, to)
	if err != nil {
		return tenantRollup{}, err
	}
	values[MetricRequests] = float64(hourly.Requests)
	values[MetricErrors5xx] = float64(hourly.Errors5xx)
	values[MetricP95Ms] = float64(hourly.P95Ms)
	values[MetricComputeRequestHours] = float64(hourly.HandlerMs) / msPerHour
	values[MetricComputeJobHours] = float64(jobs.JobMs) / msPerHour
	values[MetricFailedJobs] = float64(jobs.Failed)
	values[MetricDeadJobs] = float64(jobs.Dead)
	values[MetricActiveUsers] = float64(signIn.ActiveUsers)

	pool, err := r.pools.PoolFor(ctx, t)
	if err != nil {
		return tenantRollup{}, fmt.Errorf("tenant pool for %s: %w", t.Slug, err)
	}
	src := r.newSource(pool)
	size, err := src.DBSize(ctx)
	if err != nil {
		return tenantRollup{}, fmt.Errorf("tenant %s: %w", t.Slug, err)
	}
	values[MetricDBBytes] = float64(size)
	tables, err := src.TopTables(ctx)
	if err != nil {
		return tenantRollup{}, fmt.Errorf("tenant %s: %w", t.Slug, err)
	}
	if err := r.store.ReplaceTableSizes(ctx, t.ID, r.now(), tables); err != nil {
		return tenantRollup{}, err
	}
	emails := r.collectOptional(ctx, t, src, from, to, values)
	values[MetricEmailsSent] = emails

	if t.R2Bucket != "" && r.r2 != nil {
		if snap, err := r.r2.Measure(ctx, t.ID, t.R2Bucket); err != nil {
			slog.WarnContext(ctx, "usage rollup: r2 measure skipped", "tenant", t.Slug, "err", err)
		} else if snap != nil {
			values[MetricR2Bytes] = float64(snap.UsedBytes)
			values[MetricR2Objects] = float64(snap.ObjectCount)
		}
	}
	if err := r.store.UpsertDailyMetrics(ctx, t.ID, day, values); err != nil {
		return tenantRollup{}, err
	}
	return tenantRollup{tenant: *t, values: values, signIn: signIn}, nil
}

// collectOptional adds the audit and automation figures; a failing source is logged and skipped.
// It returns the emails-sent total (documents sent plus delivered automation emails).
func (r *Rollup) collectOptional(ctx context.Context, t *tenancy.Tenant, src tenantSource,
	from, to time.Time, values map[string]float64) float64 {
	var emails float64
	if created, err := src.RecordsCreated(ctx, from, to); err != nil {
		slog.WarnContext(ctx, "usage rollup: records created skipped", "tenant", t.Slug, "err", err)
	} else {
		for resource, n := range created {
			values[RecordsCreatedKey(resource)] = float64(n)
		}
	}
	if n, err := src.DocumentsSent(ctx, from, to); err != nil {
		slog.WarnContext(ctx, "usage rollup: documents sent skipped", "tenant", t.Slug, "err", err)
	} else {
		emails += float64(n)
	}
	if runs, sent, err := src.Deliveries(ctx, from, to); err != nil {
		slog.WarnContext(ctx, "usage rollup: automation deliveries skipped", "tenant", t.Slug, "err", err)
	} else {
		values[MetricAutomationRuns] = float64(runs)
		emails += float64(sent)
	}
	return emails
}
