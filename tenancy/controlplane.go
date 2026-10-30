package tenancy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/cache"
)

const (
	// tenantCacheTTL bounds how long a request may see a stale tenant row
	// (e.g. a suspension) on a machine other than the one that wrote it.
	tenantCacheTTL = 30 * time.Second
	// tenantCacheMax caps the per-process tenant cache.
	tenantCacheMax = 2000
	// tenantMaxStaleness bounds how old a last-known tenant row may be when it
	// is served because the control plane is unreachable.
	tenantMaxStaleness = 10 * time.Minute
)

// ErrTenantNotFound is returned when no tenant matches the lookup.
var ErrTenantNotFound = errors.New("tenant not found")

// ErrPlatformOwnerProtected is returned when an operation that would destroy
// the tenant is aimed at the platform-owner workspace — the one the platform
// admin signs in to, which must never be deleted through the platform API.
var ErrPlatformOwnerProtected = errors.New("the platform owner tenant cannot be purged")

// ControlPlane owns the connection pool to the shared control-plane database
// and exposes lookups against the tenant registry.
type ControlPlane struct {
	pool    *pgxpool.Pool
	tenants *cache.TTLCache[string, *Tenant] // TenantByID read-through cache
	// lastKnown holds the last successfully loaded row per tenant for
	// tenantMaxStaleness, served only when the control plane is unreachable.
	lastKnown *cache.TTLCache[string, lastKnownTenant]
	// load fetches a tenant row; nil means loadTenantByID. Injectable for tests.
	load func(ctx context.Context, id string) (*Tenant, error)
}

// lastKnownTenant is a tenant row plus when it was loaded, for staleness logs.
type lastKnownTenant struct {
	tenant   *Tenant
	loadedAt time.Time
}

// newLastKnownCache builds the stale-fallback cache for TenantByID.
func newLastKnownCache() *cache.TTLCache[string, lastKnownTenant] {
	return cache.NewWithMax[string, lastKnownTenant](tenantMaxStaleness, tenantCacheMax)
}

// newTenantCache builds the TenantByID cache.
func newTenantCache() *cache.TTLCache[string, *Tenant] {
	return cache.NewWithMax[string, *Tenant](tenantCacheTTL, tenantCacheMax)
}

// forgetTenant drops a tenant from the TenantByID cache. Every method that
// writes the tenants row calls it so this process never serves its own stale
// write; other processes converge within tenantCacheTTL.
func (c *ControlPlane) forgetTenant(id string) {
	if c.tenants != nil {
		c.tenants.Delete(id)
	}
	if c.lastKnown != nil {
		c.lastKnown.Delete(id)
	}
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
	return &ControlPlane{pool: pool, tenants: newTenantCache(), lastKnown: newLastKnownCache()}, nil
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
	schema_version, migration_status, COALESCE(design_version, 'v2'),
	deleted_at, hard_delete_after, created_at, updated_at,
	COALESCE(metadata::text, '{}'), COALESCE(r2_bucket, ''), schema_hash`

func scanTenant(row pgx.Row) (*Tenant, error) {
	var t Tenant
	err := row.Scan(
		&t.ID, &t.Slug, &t.DisplayName, &t.Status, &t.IsPlatformOwner,
		&t.DBName, &t.DBConnectionRef, &t.Region,
		&t.SchemaVersion, &t.MigrationStatus, &t.DesignVersion,
		&t.DeletedAt, &t.HardDeleteAfter, &t.CreatedAt, &t.UpdatedAt,
		&t.Metadata, &t.R2Bucket, &t.SchemaHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan tenant: %w", err)
	}
	return &t, nil
}

// TenantByID loads a tenant by its UUID, served from a short-TTL cache with
// concurrent misses collapsed into one query. Callers get their own copy.
// Not-found and other errors are never cached. When the load fails for a reason
// other than not-found or the caller's own cancellation (control plane down or
// slow), the last known row is served if seen within tenantMaxStaleness.
func (c *ControlPlane) TenantByID(ctx context.Context, id string) (*Tenant, error) {
	load := c.load
	if load == nil {
		load = c.loadTenantByID
	}
	if c.tenants == nil {
		return load(ctx, id)
	}
	t, err := c.tenants.GetOrLoad(id, func() (*Tenant, error) {
		t, err := load(ctx, id)
		switch {
		case err == nil:
			if c.lastKnown != nil {
				c.lastKnown.Set(id, lastKnownTenant{tenant: t.clone(), loadedAt: time.Now()})
			}
		case errors.Is(err, ErrTenantNotFound):
			if c.lastKnown != nil {
				c.lastKnown.Delete(id)
			}
		}
		return t, err
	})
	if err != nil {
		if stale, ok := c.staleTenant(ctx, id, err); ok {
			return stale, nil
		}
		return nil, err
	}
	return t.clone(), nil
}

// staleTenant returns a clone of the last known row for id when err is a
// control-plane failure (not not-found, not the caller's cancellation).
func (c *ControlPlane) staleTenant(ctx context.Context, id string, err error) (*Tenant, bool) {
	if c.lastKnown == nil || errors.Is(err, ErrTenantNotFound) ||
		ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return nil, false
	}
	last, ok := c.lastKnown.Get(id)
	if !ok {
		return nil, false
	}
	slog.WarnContext(ctx, "tenancy: control plane unavailable, serving last known tenant",
		"tenant_id", id, "err", err, "age", time.Since(last.loadedAt))
	return last.tenant.clone(), true
}

func (c *ControlPlane) loadTenantByID(ctx context.Context, id string) (*Tenant, error) {
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
