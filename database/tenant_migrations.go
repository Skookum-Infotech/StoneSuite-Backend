package database

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tenantSchemaSQL is the single canonical per-tenant schema.
// Editing the SQL file IS the migration — no numbered files, no version table.
//
//go:embed migrations/tenant/schema.sql
var tenantSchemaSQL string

// tenantSchemaLockKey is the stable advisory lock key for tenant databases.
const tenantSchemaLockKey int64 = 7369746573756974

// ApplyTenantSchema applies the full tenant schema to the given pool.
// All statements use CREATE TABLE IF NOT EXISTS / ADD COLUMN IF NOT EXISTS /
// INSERT ON CONFLICT DO NOTHING, so the call is idempotent on any DB state.
//
// Returns 1 (the constant "current schema version") for backward-compat with
// callers that store a schema version on the tenant row.
func ApplyTenantSchema(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	if err := applySchemaUnderLock(ctx, pool, tenantSchemaLockKey, tenantSchemaSQL, "tenant"); err != nil {
		return 0, err
	}
	return 1, nil
}

// ApplyTenantMigrations is an alias kept for call-site compatibility.
func ApplyTenantMigrations(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	return ApplyTenantSchema(ctx, pool)
}

var (
	tenantSchemaHashOnce  sync.Once
	tenantSchemaHashValue string
)

// TenantSchemaHash returns the hex sha256 of the embedded tenant schema.sql.
func TenantSchemaHash() string {
	tenantSchemaHashOnce.Do(func() {
		sum := sha256.Sum256([]byte(tenantSchemaSQL))
		tenantSchemaHashValue = hex.EncodeToString(sum[:])
	})
	return tenantSchemaHashValue
}
