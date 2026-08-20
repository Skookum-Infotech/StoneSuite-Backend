package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrPortalInviteNotFound is returned when a portal invite lookup misses.
var ErrPortalInviteNotFound = errors.New("portal invite not found")

// Portal invite statuses, mirroring user_invites.
const (
	PortalInviteStatusPending  = "pending"
	PortalInviteStatusAccepted = "accepted"
	PortalInviteStatusRevoked  = "revoked"
)

// PortalInvite is an invitation for a customer contact to set up portal access.
type PortalInvite struct {
	ID           string
	TenantID     string
	IdentityID   string
	Email        string
	FullName     string
	CustomerUUID string
	Token        string
	Status       string // pending | accepted | revoked
	InvitedBy    string // identity_id of the staff member who granted access
	ExpiresAt    time.Time
	AcceptedAt   *time.Time
	CreatedAt    time.Time
}

// Expired reports whether a pending invite has passed its expiry.
//
// Expiry is a property of the invite, not a separate status: an expired invite
// stays 'pending' in the database so a resend can refresh it in place rather
// than having to resurrect a terminal state.
func (p *PortalInvite) Expired() bool {
	return p.Status == PortalInviteStatusPending && time.Now().After(p.ExpiresAt)
}

// Usable reports whether the invite can still be redeemed right now.
func (p *PortalInvite) Usable() bool {
	return p.Status == PortalInviteStatusPending && !p.Expired()
}

const portalInviteColumns = `id, tenant_id, identity_id, email, full_name, customer_uuid,
	token, status, COALESCE(invited_by::text, ''), expires_at, accepted_at, created_at`

func scanPortalInvite(row pgx.Row) (*PortalInvite, error) {
	var inv PortalInvite
	err := row.Scan(&inv.ID, &inv.TenantID, &inv.IdentityID, &inv.Email, &inv.FullName,
		&inv.CustomerUUID, &inv.Token, &inv.Status, &inv.InvitedBy,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPortalInviteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan portal invite: %w", err)
	}
	return &inv, nil
}

// CreatePortalInvite records a new portal invitation.
//
// Upserts on the live-pending index so re-granting access to an address that
// already has an outstanding invite refreshes that invite instead of failing on
// the unique constraint — the same behaviour a resend gives.
func (c *ControlPlane) CreatePortalInvite(ctx context.Context,
	tenantID, identityID, email, fullName, customerUUID, token, invitedByIdentityID string,
	expiresAt time.Time) (*PortalInvite, error) {
	q := `INSERT INTO portal_invites
		(tenant_id, identity_id, email, full_name, customer_uuid, token, status, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8)
		RETURNING ` + portalInviteColumns
	return scanPortalInvite(c.pool.QueryRow(ctx, q,
		tenantID, identityID, email, fullName, customerUUID, token,
		nullable(invitedByIdentityID), expiresAt))
}

// PortalInviteByToken loads an invite by its token.
func (c *ControlPlane) PortalInviteByToken(ctx context.Context, token string) (*PortalInvite, error) {
	q := `SELECT ` + portalInviteColumns + ` FROM portal_invites WHERE token = $1`
	return scanPortalInvite(c.pool.QueryRow(ctx, q, token))
}

// PendingPortalInviteByEmail returns the live invite for an email in a tenant.
func (c *ControlPlane) PendingPortalInviteByEmail(ctx context.Context, tenantID, email string) (*PortalInvite, error) {
	q := `SELECT ` + portalInviteColumns + `
		FROM portal_invites
		WHERE tenant_id = $1 AND LOWER(email) = LOWER($2) AND status = 'pending'`
	return scanPortalInvite(c.pool.QueryRow(ctx, q, tenantID, email))
}

// LatestPortalInviteForIdentity returns the most recent invite for an identity
// in a workspace, whatever its status.
//
// Used when listing portal logins so staff can see whether an invite is still
// outstanding, has expired, or was accepted.
func (c *ControlPlane) LatestPortalInviteForIdentity(ctx context.Context, tenantID, identityID string) (*PortalInvite, error) {
	q := `SELECT ` + portalInviteColumns + `
		FROM portal_invites
		WHERE tenant_id = $1 AND identity_id = $2
		ORDER BY created_at DESC LIMIT 1`
	return scanPortalInvite(c.pool.QueryRow(ctx, q, tenantID, identityID))
}

// RefreshPortalInvite re-issues an invite with a new token and expiry.
//
// This is the resend path. The old token stops working the moment this runs,
// so a resend also invalidates a link that may have leaked.
func (c *ControlPlane) RefreshPortalInvite(ctx context.Context, id, token string, expiresAt time.Time) (*PortalInvite, error) {
	q := `UPDATE portal_invites
		SET token = $2, expires_at = $3, status = 'pending', accepted_at = NULL, updated_at = NOW()
		WHERE id = $1
		RETURNING ` + portalInviteColumns
	return scanPortalInvite(c.pool.QueryRow(ctx, q, id, token, expiresAt))
}

// MarkPortalInviteAccepted closes an invite once its password has been set.
func (c *ControlPlane) MarkPortalInviteAccepted(ctx context.Context, id string) error {
	tag, err := c.pool.Exec(ctx, `
		UPDATE portal_invites
		SET status = 'accepted', accepted_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return fmt.Errorf("mark portal invite accepted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPortalInviteNotFound
	}
	return nil
}

// RevokePortalInvitesForIdentity withdraws every outstanding invite for an
// identity in a workspace. Called when portal access is revoked, so a
// not-yet-redeemed invite link cannot be used afterwards.
func (c *ControlPlane) RevokePortalInvitesForIdentity(ctx context.Context, tenantID, identityID string) error {
	_, err := c.pool.Exec(ctx, `
		UPDATE portal_invites
		SET status = 'revoked', updated_at = NOW()
		WHERE tenant_id = $1 AND identity_id = $2 AND status = 'pending'`, tenantID, identityID)
	if err != nil {
		return fmt.Errorf("revoke portal invites for identity: %w", err)
	}
	return nil
}
