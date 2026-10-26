package database

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

// controlPlaneSchemaSQL is the single canonical control-plane schema.
// Editing the SQL file IS the migration — no numbered files, no version table.
//
//go:embed migrations/control_plane/schema.sql
var controlPlaneSchemaSQL string

// controlPlaneSchemaLockKey is the stable advisory lock key for the control-plane database.
const controlPlaneSchemaLockKey int64 = 7369746573756943

// ApplyControlPlaneSchema applies the full control-plane schema to the given
// pool. All statements use CREATE TABLE IF NOT EXISTS / ADD COLUMN IF NOT EXISTS
// so the call is safe on both a fresh database and one that already has tables.
//
// An advisory lock prevents concurrent runs across multiple app instances.
func ApplyControlPlaneSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return applySchemaUnderLock(ctx, pool, controlPlaneSchemaLockKey, controlPlaneSchemaSQL, "control-plane")
}

// ApplyControlPlaneMigrations is an alias kept for call-site compatibility.
// All callers in main.go use this name; the implementation is now schema-based.
func ApplyControlPlaneMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	return ApplyControlPlaneSchema(ctx, pool)
}
