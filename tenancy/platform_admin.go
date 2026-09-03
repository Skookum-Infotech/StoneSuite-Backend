package tenancy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/config"
)

// ErrAdminDomainNotAllowed is returned by AddPlatformAdmin when the target
// identity's email does not match config.AppConfig.PlatformAdminEmailDomain.
// Platform admin is cross-tenant, full-application access, so who can hold it
// is gated at the point of grant, not just at the point of use.
var ErrAdminDomainNotAllowed = errors.New("identity email domain not permitted for platform admin")

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
// Restricted to identities whose email matches
// config.AppConfig.PlatformAdminEmailDomain -- see ErrAdminDomainNotAllowed.
func (c *ControlPlane) AddPlatformAdmin(ctx context.Context, identityID string) error {
	identity, err := c.IdentityByID(ctx, identityID)
	if err != nil {
		return fmt.Errorf("add platform admin: resolve identity: %w", err)
	}
	if !config.AppConfig.EmailMatchesAdminDomain(identity.Email) {
		slog.Warn("security event",
			slog.String("security_event", "platform_admin_grant_denied"),
			slog.String("identity_id", identityID))
		return ErrAdminDomainNotAllowed
	}
	if _, err := c.pool.Exec(ctx,
		`INSERT INTO platform_admins (identity_id) VALUES ($1) ON CONFLICT DO NOTHING`, identityID); err != nil {
		return fmt.Errorf("add platform admin: %w", err)
	}
	return nil
}

// IsPlatformAdmin reports whether an identity currently holds platform-level
// powers. Re-validates the identity's email domain on every call (not only at
// grant time in AddPlatformAdmin) so a stale or manually-inserted
// platform_admins row cannot confer access without a data migration.
func (c *ControlPlane) IsPlatformAdmin(ctx context.Context, identityID string) (bool, error) {
	if identityID == "" {
		return false, nil
	}
	var email string
	err := c.pool.QueryRow(ctx, `
		SELECT i.email
		FROM platform_admins pa
		JOIN identities i ON i.id = pa.identity_id
		WHERE pa.identity_id = $1`, identityID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is platform admin: %w", err)
	}
	if !config.AppConfig.EmailMatchesAdminDomain(email) {
		slog.Warn("security event",
			slog.String("security_event", "platform_admin_domain_mismatch"),
			slog.String("identity_id", identityID))
		return false, nil
	}
	return true, nil
}
