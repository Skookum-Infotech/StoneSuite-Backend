package docextractjob

import (
	"context"
	"log/slog"
	"time"

	"stonesuite-backend/tenancy"
)

const (
	sweepInterval = time.Hour
	// sweepBatch bounds one DELETE per tenant; sweepMaxBatches bounds the
	// passes per tenant per sweep so one huge backlog cannot monopolise it.
	sweepBatch      = 200
	sweepMaxBatches = 10
)

// sweepLoop sweeps once at start and then hourly, until ctx is cancelled.
func (w *Worker) sweepLoop(ctx context.Context) {
	defer w.wg.Done()
	w.sweepAll(ctx)
	ticker := time.NewTicker(w.sweepIv)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweepAll(ctx)
		}
	}
}

// sweepAll purges expired staging rows (and their R2 objects) of every
// servable tenant. A failing tenant is logged and skipped.
func (w *Worker) sweepAll(ctx context.Context) {
	tenants, err := w.cp.ListTenants(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "docextract: sweep list tenants", "error", err)
		return
	}
	for i := range tenants {
		if ctx.Err() != nil {
			return
		}
		t := &tenants[i]
		if !t.Servable() {
			continue
		}
		if err := w.sweepTenant(ctx, t); err != nil {
			slog.WarnContext(ctx, "docextract: sweep tenant", "tenant", t.Slug, "error", err)
		}
	}
}

// sweepTenant deletes expired non-attached rows in bounded batches and removes
// each row's staging object. An object delete failure is logged and the sweep
// continues: the row is already gone, so the key is only an orphan to clean up.
func (w *Worker) sweepTenant(ctx context.Context, t *tenancy.Tenant) error {
	pool, err := w.router.PoolFor(ctx, t)
	if err != nil {
		return err
	}
	store := NewStore(pool)
	objects := w.stores(t.R2Bucket)
	for batch := 0; batch < sweepMaxBatches; batch++ {
		keys, err := store.SweepExpired(ctx, sweepBatch)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if objects == nil {
				continue
			}
			if err := objects.Delete(ctx, key); err != nil {
				slog.WarnContext(ctx, "docextract: delete staging object", "tenant", t.Slug, "key", key, "error", err)
			}
		}
		if len(keys) < sweepBatch {
			return nil
		}
	}
	return nil
}
