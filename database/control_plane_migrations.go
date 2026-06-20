package database

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// controlPlaneSchemaSQL is the single canonical control-plane schema.
// Editing the SQL file IS the migration — no numbered files, no version table.
//
//go:embed migrations/control_plane/schema.sql
var controlPlaneSchemaSQL string

// ApplyControlPlaneSchema applies the full control-plane schema to the given
// pool. All statements use CREATE TABLE IF NOT EXISTS / ADD COLUMN IF NOT EXISTS
// so the call is safe on both a fresh database and one that already has tables.
//
// An advisory lock prevents concurrent runs across multiple app instances.
func ApplyControlPlaneSchema(ctx context.Context, pool *pgxpool.Pool) error {
	// Lock key is a stable constant for this database role.
	if _, err := pool.Exec(ctx, `SELECT pg_advisory_lock(7369746573756943)`); err != nil {
		return fmt.Errorf("acquire control-plane schema lock: %w", err)
	}
	defer pool.Exec(ctx, `SELECT pg_advisory_unlock(7369746573756943)`) //nolint:errcheck

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin control-plane schema tx: %w", err)
	}
	if _, err := tx.Exec(ctx, controlPlaneSchemaSQL); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("apply control-plane schema: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit control-plane schema: %w", err)
	}
	return nil
}

// ApplyControlPlaneMigrations is an alias kept for call-site compatibility.
// All callers in main.go use this name; the implementation is now schema-based.
func ApplyControlPlaneMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	return ApplyControlPlaneSchema(ctx, pool)
}
