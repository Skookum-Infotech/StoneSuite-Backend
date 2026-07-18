package tenancy

import (
	"context"
	"fmt"
)

// LogPlatformAudit records a cross-tenant platform action.
func (c *ControlPlane) LogPlatformAudit(ctx context.Context, actorID, actorEmail, tenantID, action, detailsJSON string) error {
	var tID any
	if tenantID != "" {
		tID = tenantID
	}
	if detailsJSON == "" {
		detailsJSON = "{}"
	}
	if _, err := c.pool.Exec(ctx, `
		INSERT INTO platform_audit_logs (actor_identity_id, actor_email, tenant_id, action, details)
		VALUES ($1, $2, $3, $4, $5::jsonb)`,
		nullable(actorID), nullable(actorEmail), tID, action, detailsJSON); err != nil {
		return fmt.Errorf("log platform audit: %w", err)
	}
	return nil
}

// AddPlatformAdmin grants platform-level powers to an identity (idempotent).
func (c *ControlPlane) AddPlatformAdmin(ctx context.Context, identityID string) error {
	_, err := c.pool.Exec(ctx,
		`INSERT INTO platform_admins (identity_id) VALUES ($1) ON CONFLICT DO NOTHING`, identityID)
	if err != nil {
		return fmt.Errorf("add platform admin: %w", err)
	}
	return nil
}

// IsPlatformAdmin reports whether an identity has platform-level powers.
func (c *ControlPlane) IsPlatformAdmin(ctx context.Context, identityID string) (bool, error) {
	if identityID == "" {
		return false, nil
	}
	var exists bool
	err := c.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM platform_admins WHERE identity_id = $1)", identityID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("is platform admin: %w", err)
	}
	return exists, nil
}
