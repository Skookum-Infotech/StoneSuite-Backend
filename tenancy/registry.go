package tenancy

import (
	"context"
	"fmt"
	"time"
)

// ----- Tenant writes ---------------------------------------------------------

// CreateTenant inserts a new tenant in 'invited' status and returns it.
func (c *ControlPlane) CreateTenant(ctx context.Context, slug, displayName string, isPlatformOwner bool) (*Tenant, error) {
	q := `INSERT INTO tenants (slug, display_name, status, is_platform_owner)
	      VALUES ($1, $2, 'invited', $3)
	      RETURNING ` + tenantColumns
	return scanTenant(c.pool.QueryRow(ctx, q, slug, displayName, isPlatformOwner))
}

// SetTenantMetadata stores free-form onboarding metadata (company details,
// addresses, contacts) as JSONB on the tenant row. metadataJSON must be a
// valid JSON object string; pass "{}" to clear.
func (c *ControlPlane) SetTenantMetadata(ctx context.Context, id, metadataJSON string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE tenants SET metadata = $2::jsonb, updated_at = NOW() WHERE id = $1`,
		id, metadataJSON); err != nil {
		return fmt.Errorf("set tenant metadata: %w", err)
	}
	return nil
}

// SetTenantProvisioned marks a tenant active with its database routing info.
func (c *ControlPlane) SetTenantProvisioned(ctx context.Context, id, dbName, dbConnRef string, schemaVersion int) error {
	_, err := c.pool.Exec(ctx, `
		UPDATE tenants
		SET db_name = $2, db_connection_ref = $3, schema_version = $4,
		    migration_status = 'ok', status = 'active', updated_at = NOW()
		WHERE id = $1`, id, dbName, dbConnRef, schemaVersion)
	if err != nil {
		return fmt.Errorf("set tenant provisioned: %w", err)
	}
	return nil
}

// SetTenantR2Bucket stores the name of the Cloudflare R2 bucket provisioned for
// this tenant's file attachments. Called once at provisioning time.
func (c *ControlPlane) SetTenantR2Bucket(ctx context.Context, id, bucket string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE tenants SET r2_bucket = $2, updated_at = NOW() WHERE id = $1`,
		id, bucket); err != nil {
		return fmt.Errorf("set tenant r2 bucket: %w", err)
	}
	return nil
}

// SetTenantSchemaVersion updates the tracked schema version after a migration fan-out.
func (c *ControlPlane) SetTenantSchemaVersion(ctx context.Context, id string, version int) error {
	_, err := c.pool.Exec(ctx,
		`UPDATE tenants SET schema_version = $2, updated_at = NOW() WHERE id = $1`, id, version)
	if err != nil {
		return fmt.Errorf("set tenant schema version: %w", err)
	}
	return nil
}

// SetTenantDesignVersion switches a tenant's CRM data design (DesignV1/DesignV2).
// Both schemas coexist in the tenant database, so this is a behavior flag flip
// with no data migration.
func (c *ControlPlane) SetTenantDesignVersion(ctx context.Context, id, version string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE tenants SET design_version = $2, updated_at = NOW() WHERE id = $1`, id, version); err != nil {
		return fmt.Errorf("set tenant design version: %w", err)
	}
	return nil
}

// SetTenantStatus updates only the lifecycle status.
func (c *ControlPlane) SetTenantStatus(ctx context.Context, id, status string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE tenants SET status = $2, updated_at = NOW() WHERE id = $1`, id, status); err != nil {
		return fmt.Errorf("set tenant status: %w", err)
	}
	return nil
}

// SetTenantMigrationStatus records a migration outcome (ok/failed/pending).
func (c *ControlPlane) SetTenantMigrationStatus(ctx context.Context, id, status string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE tenants SET migration_status = $2, updated_at = NOW() WHERE id = $1`, id, status); err != nil {
		return fmt.Errorf("set tenant migration status: %w", err)
	}
	return nil
}

// MarkTenantDeleted soft-deletes a tenant and sets the hard-delete deadline.
func (c *ControlPlane) MarkTenantDeleted(ctx context.Context, id string, hardDeleteAfter time.Time) error {
	if _, err := c.pool.Exec(ctx, `
		UPDATE tenants
		SET status = 'deleted', deleted_at = NOW(), hard_delete_after = $2, updated_at = NOW()
		WHERE id = $1`, id, hardDeleteAfter); err != nil {
		return fmt.Errorf("mark tenant deleted: %w", err)
	}
	return nil
}

// RestoreTenant reverses a soft-delete during the grace window.
func (c *ControlPlane) RestoreTenant(ctx context.Context, id string) error {
	if _, err := c.pool.Exec(ctx, `
		UPDATE tenants
		SET status = 'active', deleted_at = NULL, hard_delete_after = NULL, updated_at = NOW()
		WHERE id = $1`, id); err != nil {
		return fmt.Errorf("restore tenant: %w", err)
	}
	return nil
}

// maxListTenants is a safety cap on unbounded SELECT; pagination should be
// added (TODO) once tenant count approaches this limit.
const maxListTenants = 1000

// ListTenants returns tenants ordered by creation time (platform admin view).
// Results are capped at maxListTenants rows to prevent unbounded scans.
func (c *ControlPlane) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := c.pool.Query(ctx, "SELECT "+tenantColumns+" FROM tenants ORDER BY created_at DESC LIMIT $1", maxListTenants)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	var out []Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// nullable converts an empty string to SQL NULL, for optional foreign-key
// and free-text columns shared by the invite and platform-admin writes.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
