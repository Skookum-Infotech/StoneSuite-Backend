package automation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/idempotency"
)

// idempotencyPruneJobName identifies the idempotency-ledger prune in logs.
const idempotencyPruneJobName = "idempotency_prune"

// idempotencyRetention is how long a create's Idempotency-Key stays
// replayable. Client retries happen within seconds; a week is generous.
const idempotencyRetention = 7 * 24 * time.Hour

// IdempotencyPrune deletes request_idempotency rows past their retention.
type IdempotencyPrune struct{}

// Name identifies the job in logs.
func (IdempotencyPrune) Name() string { return idempotencyPruneJobName }

// Run prunes the tenant's idempotency ledger as the system actor.
func (IdempotencyPrune) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	ctx = WithSystemActor(ctx)
	deleted, err := idempotency.Prune(ctx, pool, env.Now.Add(-idempotencyRetention))
	if err != nil {
		return Result{}, fmt.Errorf("idempotency prune: %w", err)
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "idempotency keys pruned", "tenant_id", env.Tenant.ID, "deleted", deleted)
	}
	return Result{}, nil
}
