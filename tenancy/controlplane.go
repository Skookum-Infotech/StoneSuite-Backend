package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrTenantNotFound is returned when no tenant matches the lookup.
var ErrTenantNotFound = errors.New("tenant not found")

// ControlPlane owns the connection pool to the shared control-plane database
// and exposes lookups against the tenant registry.
type ControlPlane struct {
	pool *pgxpool.Pool
}

// NewControlPlane opens a pool to the control-plane database at dsn.
func NewControlPlane(ctx context.Context, dsn string) (*ControlPlane, error) {
	if dsn == "" {
		return nil, errors.New("control-plane DSN is empty (set CONTROL_PLANE_DB_URL)")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse control-plane dsn: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create control-plane pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping control-plane db: %w", err)
	}
	return &ControlPlane{pool: pool}, nil
}

// Pool exposes the underlying control-plane pool for control-plane queries
// (identities, invites, etc.) implemented elsewhere.
func (c *ControlPlane) Pool() *pgxpool.Pool { return c.pool }

// Close releases the control-plane pool.
func (c *ControlPlane) Close() {
	if c.pool != nil {
		c.pool.Close()
	}
}

const tenantColumns = `
	id, slug, display_name, status, is_platform_owner,
	COALESCE(db_name, ''), COALESCE(db_connection_ref, ''), region,
	schema_version, migration_status,
	deleted_at, hard_delete_after, created_at, updated_at,
	COALESCE(metadata::text, '{}')`

func scanTenant(row pgx.Row) (*Tenant, error) {
	var t Tenant
	err := row.Scan(
		&t.ID, &t.Slug, &t.DisplayName, &t.Status, &t.IsPlatformOwner,
		&t.DBName, &t.DBConnectionRef, &t.Region,
		&t.SchemaVersion, &t.MigrationStatus,
		&t.DeletedAt, &t.HardDeleteAfter, &t.CreatedAt, &t.UpdatedAt,
		&t.Metadata,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan tenant: %w", err)
	}
	return &t, nil
}

// TenantByID loads a tenant by its UUID.
func (c *ControlPlane) TenantByID(ctx context.Context, id string) (*Tenant, error) {
	q := "SELECT " + tenantColumns + " FROM tenants WHERE id = $1"
	return scanTenant(c.pool.QueryRow(ctx, q, id))
}

// TenantBySlug loads a tenant by its slug.
func (c *ControlPlane) TenantBySlug(ctx context.Context, slug string) (*Tenant, error) {
	q := "SELECT " + tenantColumns + " FROM tenants WHERE slug = $1"
	return scanTenant(c.pool.QueryRow(ctx, q, slug))
}

// PlatformOwnerTenant loads the single platform-owner tenant (your company).
// Returns ErrTenantNotFound if none has been designated.
func (c *ControlPlane) PlatformOwnerTenant(ctx context.Context) (*Tenant, error) {
	q := "SELECT " + tenantColumns + " FROM tenants WHERE is_platform_owner = TRUE LIMIT 1"
	return scanTenant(c.pool.QueryRow(ctx, q))
}
