package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrPortalLinkNotFound is returned when a portal link lookup misses.
var ErrPortalLinkNotFound = errors.New("portal link not found")

// PortalLink is one identity's membership of one workspace as a portal customer.
//
// TenantName and TenantSlug are joined from tenants for display in the
// workspace switcher; they are not stored on the link itself.
type PortalLink struct {
	ID         string
	IdentityID string
	TenantID   string
	Kind       string
	Status     string
	TenantName string
	TenantSlug string
	CreatedAt  time.Time
}

// CreatePortalLink links an identity to a tenant as a portal customer.
//
// Idempotent by design: re-granting access to an identity whose link was
// revoked reactivates that row rather than failing on the unique constraint,
// so a customer who lost access and was later re-enabled keeps one audit row
// instead of accumulating one per grant.
func (c *ControlPlane) CreatePortalLink(ctx context.Context, identityID, tenantID string) (*PortalLink, error) {
	var l PortalLink
	err := c.pool.QueryRow(ctx, `
		INSERT INTO identity_tenants (identity_id, tenant_id, kind, status)
		VALUES ($1, $2, 'portal', 'active')
		ON CONFLICT (identity_id, tenant_id) DO UPDATE
			SET status = 'active', revoked_at = NULL, updated_at = NOW()
		RETURNING id, identity_id, tenant_id, kind, status, created_at`,
		identityID, tenantID,
	).Scan(&l.ID, &l.IdentityID, &l.TenantID, &l.Kind, &l.Status, &l.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create portal link: %w", err)
	}
	return &l, nil
}

// PortalTenantsForIdentity lists every workspace the identity may enter as a
// portal customer, newest link last. Soft-deleted tenants are excluded here;
// the caller must still check Tenant.Servable() before offering a workspace in
// the switcher, since suspended or still-provisioning tenants would be rejected
// by the tenancy resolver on the next request.
func (c *ControlPlane) PortalTenantsForIdentity(ctx context.Context, identityID string) ([]PortalLink, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT it.id, it.identity_id, it.tenant_id, it.kind, it.status,
		       t.display_name, t.slug, it.created_at
		FROM identity_tenants it
		JOIN tenants t ON t.id = it.tenant_id
		WHERE it.identity_id = $1
		  AND it.kind   = 'portal'
		  AND it.status = 'active'
		  AND t.deleted_at IS NULL
		ORDER BY it.created_at`, identityID)
	if err != nil {
		return nil, fmt.Errorf("portal tenants for identity: %w", err)
	}
	defer rows.Close()

	links := []PortalLink{}
	for rows.Next() {
		var l PortalLink
		if err := rows.Scan(&l.ID, &l.IdentityID, &l.TenantID, &l.Kind, &l.Status,
			&l.TenantName, &l.TenantSlug, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan portal link: %w", err)
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate portal links: %w", err)
	}
	return links, nil
}

// PortalLinkActive reports whether the identity currently has portal access to
// the tenant. This is the authorization check behind the workspace switcher —
// it is what stops a portal session re-minting a token for a workspace it was
// never granted.
func (c *ControlPlane) PortalLinkActive(ctx context.Context, identityID, tenantID string) (bool, error) {
	var exists bool
	err := c.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM identity_tenants
			WHERE identity_id = $1 AND tenant_id = $2
			  AND kind = 'portal' AND status = 'active')`,
		identityID, tenantID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("portal link active: %w", err)
	}
	return exists, nil
}

// HasAnyPortalLink reports whether the identity is a portal customer anywhere.
//
// Used to keep the two credential surfaces from crossing: the shared
// identities.password_reset_token column serves staff setup, forgot-password
// and portal setup alike, so each portal token endpoint asserts this before
// acting on a token, and the staff login path asserts the converse.
func (c *ControlPlane) HasAnyPortalLink(ctx context.Context, identityID string) (bool, error) {
	var exists bool
	err := c.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM identity_tenants
			WHERE identity_id = $1 AND kind = 'portal' AND status = 'active')`,
		identityID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("has any portal link: %w", err)
	}
	return exists, nil
}

// RevokePortalLink withdraws portal access to one workspace.
//
// Never deletes: the row is the audit record of access having been granted.
func (c *ControlPlane) RevokePortalLink(ctx context.Context, identityID, tenantID string) error {
	tag, err := c.pool.Exec(ctx, `
		UPDATE identity_tenants
		SET status = 'revoked', revoked_at = NOW(), updated_at = NOW()
		WHERE identity_id = $1 AND tenant_id = $2 AND status = 'active'`,
		identityID, tenantID)
	if err != nil {
		return fmt.Errorf("revoke portal link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPortalLinkNotFound
	}
	return nil
}
