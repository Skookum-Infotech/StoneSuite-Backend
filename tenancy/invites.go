package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrInviteNotFound / ErrUserInviteNotFound are returned when a lookup misses.
var (
	ErrInviteNotFound     = errors.New("invite not found")
	ErrUserInviteNotFound = errors.New("user invite not found")
)

// Invite is a platform onboarding invitation for a tenant.
type Invite struct {
	ID           string
	TenantID     string
	ContactEmail string
	Token        string
	Status       string
	ExpiresAt    time.Time
	AcceptedAt   *time.Time
	CreatedAt    time.Time
}

// ----- Invite writes/reads ---------------------------------------------------

// CreateInvite inserts a pending invite for a tenant.
func (c *ControlPlane) CreateInvite(ctx context.Context, tenantID, email, token string, expiresAt time.Time) (*Invite, error) {
	var inv Invite
	err := c.pool.QueryRow(ctx, `
		INSERT INTO tenant_invites (tenant_id, contact_email, token, status, expires_at, sent_at)
		VALUES ($1, $2, $3, 'pending', $4, NOW())
		RETURNING id, tenant_id, contact_email, token, status, expires_at, accepted_at, created_at`,
		tenantID, email, token, expiresAt,
	).Scan(&inv.ID, &inv.TenantID, &inv.ContactEmail, &inv.Token, &inv.Status,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create invite: %w", err)
	}
	return &inv, nil
}

// InviteByToken loads an invite by its token.
func (c *ControlPlane) InviteByToken(ctx context.Context, token string) (*Invite, error) {
	var inv Invite
	err := c.pool.QueryRow(ctx, `
		SELECT id, tenant_id, contact_email, token, status, expires_at, accepted_at, created_at
		FROM tenant_invites WHERE token = $1`, token,
	).Scan(&inv.ID, &inv.TenantID, &inv.ContactEmail, &inv.Token, &inv.Status,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInviteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("invite by token: %w", err)
	}
	return &inv, nil
}

// MarkInviteAccepted flips an invite to accepted.
func (c *ControlPlane) MarkInviteAccepted(ctx context.Context, id string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE tenant_invites SET status = 'accepted', accepted_at = NOW(), updated_at = NOW() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("mark invite accepted: %w", err)
	}
	return nil
}

// ListInvitesByTenant returns a tenant's invites, newest first.
func (c *ControlPlane) ListInvitesByTenant(ctx context.Context, tenantID string) ([]Invite, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT id, tenant_id, contact_email, token, status, expires_at, accepted_at, created_at
		FROM tenant_invites WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list invites by tenant: %w", err)
	}
	defer rows.Close()

	var out []Invite
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.ID, &inv.TenantID, &inv.ContactEmail, &inv.Token, &inv.Status,
			&inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invite: %w", err)
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// LatestInviteForTenant returns the most recent invite for a tenant, or nil if
// none exists (without treating "none" as an error).
func (c *ControlPlane) LatestInviteForTenant(ctx context.Context, tenantID string) (*Invite, error) {
	invites, err := c.ListInvitesByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if len(invites) == 0 {
		return nil, nil
	}
	return &invites[0], nil
}

// RefreshInvite re-issues an existing invite with a fresh token + expiry and
// resets it to pending (the "resend / retry" path). updated_at/sent_at bump.
func (c *ControlPlane) RefreshInvite(ctx context.Context, id, token string, expiresAt time.Time) (*Invite, error) {
	var inv Invite
	err := c.pool.QueryRow(ctx, `
		UPDATE tenant_invites
		SET token = $2, expires_at = $3, status = 'pending', accepted_at = NULL,
		    sent_at = NOW(), updated_at = NOW()
		WHERE id = $1
		RETURNING id, tenant_id, contact_email, token, status, expires_at, accepted_at, created_at`,
		id, token, expiresAt,
	).Scan(&inv.ID, &inv.TenantID, &inv.ContactEmail, &inv.Token, &inv.Status,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("refresh invite: %w", err)
	}
	return &inv, nil
}

// ============================================================================
// Workspace user invites (per-tenant, stored in control plane for token lookup)
// ============================================================================

// UserInvite represents a pending invitation for a colleague to join a tenant workspace.
type UserInvite struct {
	ID            string
	TenantID      string
	Email         string
	FullName      string
	InitialRoleID string // may be empty; references tenant-DB roles.id (no cross-DB FK)
	Token         string
	Status        string // pending | accepted | revoked
	InvitedBy     string // identity_id of the inviter (may be empty)
	ExpiresAt     time.Time
	AcceptedAt    *time.Time
	CreatedAt     time.Time
}

const userInviteColumns = `id, tenant_id, email, full_name,
	COALESCE(initial_role_id::text,''), token, status,
	COALESCE(invited_by::text,''), expires_at, accepted_at, created_at`

func scanUserInvite(row pgx.Row) (*UserInvite, error) {
	var inv UserInvite
	err := row.Scan(
		&inv.ID, &inv.TenantID, &inv.Email, &inv.FullName,
		&inv.InitialRoleID, &inv.Token, &inv.Status,
		&inv.InvitedBy, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserInviteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user invite: %w", err)
	}
	return &inv, nil
}

// CreateUserInvite records a new workspace user invite in the control plane.
func (c *ControlPlane) CreateUserInvite(
	ctx context.Context,
	tenantID, email, fullName, initialRoleID, token, invitedByIdentityID string,
	expiresAt time.Time,
) (*UserInvite, error) {
	q := `INSERT INTO user_invites
		(tenant_id, email, full_name, initial_role_id, token, status, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7)
		RETURNING ` + userInviteColumns
	return scanUserInvite(c.pool.QueryRow(ctx, q,
		tenantID, email, fullName,
		nullable(initialRoleID),
		token,
		nullable(invitedByIdentityID),
		expiresAt,
	))
}

// UserInviteByToken loads a user invite by its token.
func (c *ControlPlane) UserInviteByToken(ctx context.Context, token string) (*UserInvite, error) {
	q := `SELECT ` + userInviteColumns + ` FROM user_invites WHERE token = $1`
	return scanUserInvite(c.pool.QueryRow(ctx, q, token))
}

// UserInviteByID loads a user invite by its primary key.
func (c *ControlPlane) UserInviteByID(ctx context.Context, id string) (*UserInvite, error) {
	q := `SELECT ` + userInviteColumns + ` FROM user_invites WHERE id = $1`
	return scanUserInvite(c.pool.QueryRow(ctx, q, id))
}

// PendingUserInviteByEmail returns the pending invite for an email in a tenant, if any.
// Returns ErrUserInviteNotFound when none exists.
func (c *ControlPlane) PendingUserInviteByEmail(ctx context.Context, tenantID, email string) (*UserInvite, error) {
	q := `SELECT ` + userInviteColumns + ` FROM user_invites
		WHERE tenant_id = $1 AND LOWER(email) = LOWER($2) AND status = 'pending'
		LIMIT 1`
	return scanUserInvite(c.pool.QueryRow(ctx, q, tenantID, email))
}

// ListUserInvitesByTenant returns all user invites for a tenant, newest first.
func (c *ControlPlane) ListUserInvitesByTenant(ctx context.Context, tenantID string) ([]UserInvite, error) {
	q := `SELECT ` + userInviteColumns + `
		FROM user_invites WHERE tenant_id = $1 ORDER BY created_at DESC`
	rows, err := c.pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list user invites: %w", err)
	}
	defer rows.Close()
	var out []UserInvite
	for rows.Next() {
		inv, err := scanUserInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

// MarkUserInviteAccepted transitions a user invite to accepted status.
func (c *ControlPlane) MarkUserInviteAccepted(ctx context.Context, id string) error {
	_, err := c.pool.Exec(ctx,
		`UPDATE user_invites SET status = 'accepted', accepted_at = NOW(), updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("mark user invite accepted: %w", err)
	}
	return nil
}

// RevokeUserInvite cancels a pending user invite.
func (c *ControlPlane) RevokeUserInvite(ctx context.Context, id string) error {
	tag, err := c.pool.Exec(ctx,
		`UPDATE user_invites SET status = 'revoked', updated_at = NOW() WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return fmt.Errorf("revoke user invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserInviteNotFound
	}
	return nil
}

// RefreshUserInvite re-issues a user invite with a new token and expiry (resend path).
func (c *ControlPlane) RefreshUserInvite(ctx context.Context, id, token string, expiresAt time.Time) (*UserInvite, error) {
	q := `UPDATE user_invites
		SET token = $2, expires_at = $3, status = 'pending', accepted_at = NULL, updated_at = NOW()
		WHERE id = $1
		RETURNING ` + userInviteColumns
	return scanUserInvite(c.pool.QueryRow(ctx, q, id, token, expiresAt))
}
