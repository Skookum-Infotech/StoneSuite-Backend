package domainevent

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DrainAll drains every registered consumer in name order, handling at most
// maxEvents events in total (<=0 means MaxEventsPerTick) for per-tenant fairness.
// One consumer's error does not stop the others; errors are joined. Busy is true
// if any consumer was busy, Failed if any handler failed.
func DrainAll(ctx context.Context, pool *pgxpool.Pool, reg *Registry, maxEvents int) (DrainResult, error) {
	if maxEvents <= 0 {
		maxEvents = MaxEventsPerTick
	}
	var (
		total DrainResult
		errs  []error
	)
	for _, name := range reg.Names() {
		remaining := maxEvents - total.Handled - total.Skipped
		if remaining <= 0 {
			break
		}
		batch := min(DefaultBatch, remaining)
		r, err := Drain(ctx, pool, reg, name, batch)
		if err != nil {
			errs = append(errs, fmt.Errorf("consumer %s: %w", name, err))
		}
		total.Handled += r.Handled
		total.Skipped += r.Skipped
		total.Busy = total.Busy || r.Busy
		total.Failed = total.Failed || r.Failed
	}
	return total, errors.Join(errs...)
}

// DrainNow drains all consumers with the default per-tick cap; it is the seam a
// future background worker calls once per tenant per tick.
func DrainNow(ctx context.Context, pool *pgxpool.Pool, reg *Registry) (DrainResult, error) {
	return DrainAll(ctx, pool, reg, MaxEventsPerTick)
}
