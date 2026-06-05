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

// controlPlaneMigrationsFS embeds the control-plane "up" migration files.
// Applied once to the shared stonesuite_cp database on startup.
//
//go:embed migrations/control_plane/*.up.sql
var controlPlaneMigrationsFS embed.FS

type cpMigration struct {
	version int
	name    string
	sql     string
}

func loadControlPlaneMigrations() ([]cpMigration, error) {
	entries, err := controlPlaneMigrationsFS.ReadDir("migrations/control_plane")
	if err != nil {
		return nil, fmt.Errorf("read embedded control-plane migrations: %w", err)
	}
	var migs []cpMigration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix := strings.SplitN(name, "_", 2)[0]
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("bad cp migration filename %q: %w", name, err)
		}
		b, err := controlPlaneMigrationsFS.ReadFile("migrations/control_plane/" + name)
		if err != nil {
			return nil, fmt.Errorf("read cp migration %q: %w", name, err)
		}
		migs = append(migs, cpMigration{version: version, name: name, sql: string(b)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}

// ApplyControlPlaneMigrations brings the shared control-plane database up to
// the latest embedded schema. Tracks applied versions in a cp_schema_version
// table and applies each pending migration inside its own transaction.
// Idempotent: safe to call on every startup.
func ApplyControlPlaneMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS cp_schema_version (
			version    INT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("ensure cp_schema_version table: %w", err)
	}

	var current int
	if err := pool.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM cp_schema_version").Scan(&current); err != nil {
		return fmt.Errorf("read current cp schema version: %w", err)
	}

	migs, err := loadControlPlaneMigrations()
	if err != nil {
		return err
	}

	for _, m := range migs {
		if m.version <= current {
			continue
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for cp migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply cp migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO cp_schema_version (version) VALUES ($1)", m.version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record cp migration %s: %w", m.name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit cp migration %s: %w", m.name, err)
		}
		current = m.version
	}
	return nil
}
