package database

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tenantSchemaSQL is the single canonical per-tenant schema.
// Editing the SQL file IS the migration — no numbered files, no version table.
//
//go:embed migrations/tenant/schema.sql
var tenantSchemaSQL string

// ApplyTenantSchema applies the full tenant schema to the given pool.
// All statements use CREATE TABLE IF NOT EXISTS / ADD COLUMN IF NOT EXISTS /
// INSERT ON CONFLICT DO NOTHING, so the call is idempotent on any DB state.
//
// Returns 1 (the constant "current schema version") for backward-compat with
// callers that store a schema version on the tenant row.
func ApplyTenantSchema(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	// Stable advisory lock constant for tenant databases.
	if _, err := pool.Exec(ctx, `SELECT pg_advisory_lock(7369746573756974)`); err != nil {
		return 0, fmt.Errorf("acquire tenant schema lock: %w", err)
	}
	defer pool.Exec(ctx, `SELECT pg_advisory_unlock(7369746573756974)`) //nolint:errcheck

	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tenant schema tx: %w", err)
	}
	if _, err := tx.Exec(ctx, tenantSchemaSQL); err != nil {
		_ = tx.Rollback(ctx)
		return 0, fmt.Errorf("apply tenant schema: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit tenant schema: %w", err)
	}
	return 1, nil
}

// ApplyTenantMigrations is an alias kept for call-site compatibility.
func ApplyTenantMigrations(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	return ApplyTenantSchema(ctx, pool)
}

// LatestTenantSchemaVersion returns 1 — the schema is now a single canonical
// file, so there is no numeric version. Callers that compare against this
// constant to decide whether a tenant needs updating will always get a match.
func LatestTenantSchemaVersion() (int, error) {
	return 1, nil
}
