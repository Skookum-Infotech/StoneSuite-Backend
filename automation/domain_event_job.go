package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/config"
	"stonesuite-backend/domainevent"
)

// domainEventJobName identifies the domain-event drain job in logs.
const domainEventJobName = "domain_event_drain"

// DomainEventDrain drains the tenant's domain-event outbox through the consumers
// registered at boot, then prunes old events every consumer has already passed.
type DomainEventDrain struct {
	deps Deps
	// retentionDays overrides config.AppConfig.DomainEventRetentionDays when > 0.
	retentionDays int
}

// Name identifies the job in logs.
func (*DomainEventDrain) Name() string { return domainEventJobName }

// Run drains registered consumers as the system actor, then always prunes; a
// prune error is joined into the returned error without hiding the drain result.
func (j *DomainEventDrain) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	ctx = WithSystemActor(ctx)
	var (
		res  Result
		errs []error
	)
	if reg := j.deps.Events; reg != nil && len(reg.Names()) > 0 {
		dr, err := domainevent.DrainAll(ctx, pool, reg, 0)
		res = Result{Transitioned: dr.Handled, Skipped: dr.Skipped}
		if err != nil {
			errs = append(errs, fmt.Errorf("domain event drain: %w", err))
		}
	}
	days := j.retentionDays
	if days <= 0 {
		days = config.AppConfig.DomainEventRetentionDays
	}
	if days <= 0 {
		days = defaultDomainEventRetentionDays // unset config must not error every tick
	}
	pruned, err := domainevent.Prune(ctx, pool, days)
	if err != nil {
		errs = append(errs, fmt.Errorf("domain event prune: %w", err))
	} else if pruned > 0 {
		slog.InfoContext(ctx, "domain events pruned", "tenant_id", env.Tenant.ID, "pruned", pruned)
	}
	return res, errors.Join(errs...)
}

// defaultDomainEventRetentionDays is the pruning window used when neither the
// job nor config.AppConfig sets one.
const defaultDomainEventRetentionDays = 30
