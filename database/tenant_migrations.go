package database

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tenantMigrationsFS embeds the per-tenant "up" migration files. They are
// applied to each tenant's isolated database at provisioning time and by the
// migration runner when the schema changes. We embed only .up.sql; rollbacks
// for tenant DBs are handled by restoring/branching, not down-migrations.
//
//go:embed migrations/tenant/*.up.sql
var tenantMigrationsFS embed.FS

type tenantMigration struct {
	version int
	name    string
	sql     string
}

// loadTenantMigrations reads embedded tenant migrations ordered by version
// (the numeric prefix, e.g. 000001_...).
func loadTenantMigrations() ([]tenantMigration, error) {
	entries, err := tenantMigrationsFS.ReadDir("migrations/tenant")
	if err != nil {
		return nil, fmt.Errorf("read embedded tenant migrations: %w", err)
	}
	var migs []tenantMigration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix := strings.SplitN(name, "_", 2)[0]
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("bad migration filename %q: %w", name, err)
		}
		b, err := tenantMigrationsFS.ReadFile("migrations/tenant/" + name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		migs = append(migs, tenantMigration{version: version, name: name, sql: string(b)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}

// LatestTenantSchemaVersion returns the highest embedded tenant migration version.
func LatestTenantSchemaVersion() (int, error) {
	migs, err := loadTenantMigrations()
	if err != nil {
		return 0, err
	}
	if len(migs) == 0 {
		return 0, nil
	}
	return migs[len(migs)-1].version, nil
}

// ApplyTenantMigrations brings a tenant database up to the latest embedded
// schema version. It tracks applied versions in a tenant-local schema_version
// table and applies each pending migration inside its own transaction
// (all-or-nothing). Returns the resulting schema version. Idempotent: running
// it again on a current DB is a no-op.
func ApplyTenantMigrations(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_version (
			version    INT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return 0, fmt.Errorf("ensure schema_version table: %w", err)
	}

	var current int
	if err := pool.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&current); err != nil {
		return 0, fmt.Errorf("read current schema version: %w", err)
	}

	migs, err := loadTenantMigrations()
	if err != nil {
		return 0, err
	}

	for _, m := range migs {
		if m.version <= current {
			continue
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return current, fmt.Errorf("begin tx for migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return current, fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_version (version) VALUES ($1)", m.version); err != nil {
			_ = tx.Rollback(ctx)
			return current, fmt.Errorf("record migration %s: %w", m.name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return current, fmt.Errorf("commit migration %s: %w", m.name, err)
		}
		current = m.version
	}
	return current, nil
}
