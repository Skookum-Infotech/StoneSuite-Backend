package automation

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// expiryJobName identifies the expiry reminder job in logs.
const expiryJobName = "expiry_reminders"

// ExpiryReminders reminds quote and estimate owners before valid-until dates.
type ExpiryReminders struct{ deps Deps }

// Name identifies the job in logs.
func (*ExpiryReminders) Name() string { return expiryJobName }

// Run plans and drains each spec in turn; one spec failing never stops the other.
func (j *ExpiryReminders) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	var total Result
	var errs []error
	for _, spec := range []expirySpec{quoteSpec, estimateSpec} {
		res, err := j.runSpec(ctx, pool, env, spec)
		total.Transitioned += res.Transitioned
		total.Skipped += res.Skipped
		total.Sent += res.Sent
		total.Failed += res.Failed
		if err != nil {
			errs = append(errs, err)
		}
	}
	return total, errors.Join(errs...)
}

// runSpec runs one spec: a missing or disabled rule is a no-op that writes nothing.
func (j *ExpiryReminders) runSpec(ctx context.Context, pool *pgxpool.Pool, env JobEnv, spec expirySpec) (Result, error) {
	rule, enabled, err := loadRule(ctx, pool, spec.RuleKey)
	if err != nil {
		return Result{}, fmt.Errorf("%s expiry: %w", spec.Resource, err)
	}
	if !enabled {
		return Result{}, nil
	}
	_, skipped, err := planExpiry(ctx, pool, env, spec, rule)
	if err != nil {
		return Result{}, fmt.Errorf("%s expiry plan: %w", spec.Resource, err)
	}
	res, err := drain(ctx, pool, env, j.deps.Send, expiryRenderer{pool: pool, deps: j.deps, spec: spec}, rule.ID)
	res.Skipped += skipped
	if err != nil {
		return res, fmt.Errorf("%s expiry drain: %w", spec.Resource, err)
	}
	return res, nil
}
