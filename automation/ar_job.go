package automation

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ARDunning is the accounts-receivable payment-reminder job.
type ARDunning struct{ deps Deps }

// Name identifies the job in logs.
func (*ARDunning) Name() string { return RuleKeyARDunning }

// Run plans due reminder stages then drains them as one digest per customer.
// A missing or disabled rule is a no-op that writes nothing.
func (j *ARDunning) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	rule, enabled, err := loadRule(ctx, pool, RuleKeyARDunning)
	if err != nil {
		return Result{}, fmt.Errorf("ar dunning: %w", err)
	}
	if !enabled {
		return Result{}, nil
	}
	_, skipped, err := j.plan(ctx, pool, env, rule)
	if err != nil {
		return Result{}, fmt.Errorf("ar dunning plan: %w", err)
	}
	res, err := drain(ctx, pool, env, j.deps.Send, arRenderer{pool: pool, deps: j.deps}, rule.ID)
	res.Skipped += skipped
	if err != nil {
		return res, fmt.Errorf("ar dunning drain: %w", err)
	}
	return res, nil
}
