package automation

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// digestJobName identifies the daily digest job in logs.
const digestJobName = "daily_digest"

// DailyDigest sends each opted-in user one summary email a day.
type DailyDigest struct{ deps Deps }

// Name identifies the job in logs.
func (*DailyDigest) Name() string { return digestJobName }

// Run plans and drains the daily_digest rule; a missing or disabled rule is a
// no-op that writes nothing.
func (j *DailyDigest) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	rule, enabled, err := loadRule(ctx, pool, RuleKeyDailyDigest)
	if err != nil {
		return Result{}, fmt.Errorf("daily digest: %w", err)
	}
	if !enabled {
		return Result{}, nil
	}
	if _, err := planDigests(ctx, pool, env, rule); err != nil {
		return Result{}, fmt.Errorf("daily digest plan: %w", err)
	}
	res, err := drain(ctx, pool, env, j.deps.Send, digestRenderer{pool: pool}, rule.ID)
	if err != nil {
		return res, fmt.Errorf("daily digest drain: %w", err)
	}
	return res, nil
}
