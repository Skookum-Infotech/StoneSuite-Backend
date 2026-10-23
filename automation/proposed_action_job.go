package automation

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/config"
	"stonesuite-backend/proposedaction"
)

// proposedActionSweepJobName identifies the proposed-action sweep in logs.
const proposedActionSweepJobName = "proposed_action_sweep"

// defaultProposedActionRetentionDays is the pruning window used when neither
// the job nor config.AppConfig sets one.
const defaultProposedActionRetentionDays = 7

// ProposedActionSweep expires overdue pending proposals and prunes old resolved ones.
type ProposedActionSweep struct {
	// retentionDays overrides config.AppConfig.ProposedActionRetentionDays when > 0.
	retentionDays int
}

// Name identifies the job in logs.
func (*ProposedActionSweep) Name() string { return proposedActionSweepJobName }

// Run sweeps the tenant's proposed actions as the system actor.
func (j *ProposedActionSweep) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	ctx = WithSystemActor(ctx)
	days := j.retentionDays
	if days <= 0 {
		days = config.AppConfig.ProposedActionRetentionDays
	}
	if days <= 0 {
		days = defaultProposedActionRetentionDays
	}
	expired, deleted, err := proposedaction.NewStore(pool, proposedaction.NewRegistry(), 0).Sweep(ctx, pool, days)
	if err != nil {
		return Result{}, fmt.Errorf("proposed action sweep: %w", err)
	}
	if expired > 0 || deleted > 0 {
		slog.InfoContext(ctx, "proposed actions swept", "tenant_id", env.Tenant.ID, "expired", expired, "deleted", deleted)
	}
	return Result{Transitioned: int(expired)}, nil
}
