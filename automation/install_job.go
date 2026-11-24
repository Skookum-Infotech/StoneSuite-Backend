package automation

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// installJobName identifies the install-tomorrow reminder job in logs.
const installJobName = "install_reminders"

// InstallReminders reminds crew members the day before a scheduled site visit.
type InstallReminders struct{ deps Deps }

// Name identifies the job in logs.
func (*InstallReminders) Name() string { return installJobName }

// Run plans and drains the install_tomorrow rule; a missing or disabled rule is
// a no-op that writes nothing.
func (j *InstallReminders) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	rule, enabled, err := loadRule(ctx, pool, RuleKeyInstallTomorrow)
	if err != nil {
		return Result{}, fmt.Errorf("install reminders: %w", err)
	}
	if !enabled {
		return Result{}, nil
	}
	_, skipped, err := planInstall(ctx, pool, env, rule)
	if err != nil {
		return Result{}, fmt.Errorf("install reminders plan: %w", err)
	}
	res, err := drain(ctx, pool, env, j.deps.Send, installRenderer{pool: pool, deps: j.deps}, rule.ID)
	res.Skipped += skipped
	if err != nil {
		return res, fmt.Errorf("install reminders drain: %w", err)
	}
	return res, nil
}
