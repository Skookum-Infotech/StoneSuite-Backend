package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"stonesuite-backend/database"
	"stonesuite-backend/tenancy"
)

const (
	// tenantMigrateTimeout bounds one tenant's boot-time schema apply.
	tenantMigrateTimeout = 2 * time.Minute
	// bootMigrateConcurrency bounds how many tenants migrate in parallel at boot.
	bootMigrateConcurrency = 4
)

// tenantMigrateFunc migrates one tenant; injected so the runner is testable.
type tenantMigrateFunc func(ctx context.Context, t tenancy.Tenant) error

// bootMigrations is the handle for the background boot-migration run.
type bootMigrations struct {
	done chan struct{}
}

// Stop blocks until the background migration run has exited (its context must already be cancelled).
func (b *bootMigrations) Stop() {
	if b == nil {
		return
	}
	<-b.done
}

// selectStaleTenants returns active, provisioned tenants whose recorded schema
// hash differs from currentHash or whose migration_status is not ok. A tenant
// with a matching hash and status ok is skipped.
func selectStaleTenants(tenants []tenancy.Tenant, currentHash string) []tenancy.Tenant {
	var stale []tenancy.Tenant
	for _, t := range tenants {
		if t.Status != tenancy.StatusActive || t.DBName == "" {
			continue
		}
		if t.SchemaHash != currentHash || t.MigrationStatus != tenancy.MigrationOK {
			stale = append(stale, t)
		}
	}
	return stale
}

// staleTenants lists the tenants needing a schema apply and gates each one
// (gate.Begin) so requests get a 503 until its migration finishes.
//
// Eligibility is status==active && db_name set, not Servable(): a tenant whose
// migration_status is stuck short of "ok" still has a real database and only
// this path can promote it back to "ok".
func staleTenants(ctx context.Context, cp *tenancy.ControlPlane, gate *tenancy.MigrationGate) ([]tenancy.Tenant, error) {
	tenants, err := cp.ListTenants(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenants for boot migration: %w", err)
	}
	stale := selectStaleTenants(tenants, database.TenantSchemaHash())
	for _, t := range stale {
		gate.Begin(t.ID)
	}
	return stale, nil
}

// runBootMigrations migrates the stale tenants in the background and returns a
// handle whose Stop waits for the run to exit. Cancel ctx to abort.
func runBootMigrations(ctx context.Context, cp *tenancy.ControlPlane, router *tenancy.Router, gate *tenancy.MigrationGate, stale []tenancy.Tenant, onMigrated func(tenantID string)) *bootMigrations {
	b := &bootMigrations{done: make(chan struct{})}
	hash := database.TenantSchemaHash()
	migrate := func(ctx context.Context, t tenancy.Tenant) error {
		if err := migrateTenant(ctx, cp, router, t); err != nil {
			return err
		}
		if err := cp.SetTenantSchemaHash(ctx, t.ID, hash); err != nil {
			return fmt.Errorf("record schema hash: %w", err)
		}
		return nil
	}
	go func() {
		defer close(b.done)
		runMigrations(ctx, gate, stale, bootMigrateConcurrency, migrate, onMigrated)
	}()
	return b
}

// runMigrations runs migrate for every stale tenant with at most concurrency in
// flight and returns when all have finished or ctx is cancelled. Every tenant
// is released from the gate whether or not it migrated; onMigrated fires only
// on success.
func runMigrations(ctx context.Context, gate *tenancy.MigrationGate, stale []tenancy.Tenant, concurrency int, migrate tenantMigrateFunc, onMigrated func(tenantID string)) {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, t := range stale {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			gate.Done(t.ID)
			continue
		}
		wg.Add(1)
		go func(t tenancy.Tenant) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := migrate(ctx, t); err != nil {
				gate.Done(t.ID)
				slog.Error("boot migration failed", "tenant", t.Slug, "error", err)
				return
			}
			// Release the gate before signalling so the rescan sees the tenant.
			gate.Done(t.ID)
			if onMigrated != nil {
				onMigrated(t.ID)
			}
		}(t)
	}
	wg.Wait()
}

// migrateTenant migrates one tenant under its own tenantMigrateTimeout, so a
// slow or lock-blocked tenant fails on its own instead of stalling the rest.
func migrateTenant(ctx context.Context, cp *tenancy.ControlPlane, router *tenancy.Router, t tenancy.Tenant) error {
	ctx, cancel := context.WithTimeout(ctx, tenantMigrateTimeout)
	defer cancel()
	pool, err := router.PoolFor(ctx, &t)
	if err != nil {
		return fmt.Errorf("tenant %s: pool: %w", t.Slug, err)
	}
	ver, err := database.ApplyTenantMigrations(ctx, pool)
	if err != nil {
		return fmt.Errorf("tenant %s: apply migrations: %w", t.Slug, err)
	}
	if err := cp.SetTenantSchemaVersion(ctx, t.ID, ver); err != nil {
		slog.Error("migrate-all: update schema version failed", "tenant", t.Slug, "error", err)
	} else {
		slog.Info("migrate-all: tenant migrated", "tenant", t.Slug, "version", ver)
	}
	if t.MigrationStatus != tenancy.MigrationOK {
		if err := cp.SetTenantMigrationStatus(ctx, t.ID, tenancy.MigrationOK); err != nil {
			slog.Error("migrate-all: failed to recover migration_status to ok", "tenant", t.Slug, "error", err)
		} else {
			slog.Info("migrate-all: migration_status recovered to ok", "tenant", t.Slug)
		}
	}
	if empty, verr := database.ValidateLookupSeeds(ctx, pool); verr != nil {
		slog.Warn("migrate-all: lookup seed check failed", "tenant", t.Slug, "error", verr)
	} else if len(empty) > 0 {
		slog.Warn("migrate-all: unseeded CRM lookup tables", "tenant", t.Slug, "tables", empty)
	}
	return nil
}

// signalRescan nudges the RAG indexer to rescan tenants without ever blocking.
func signalRescan(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
