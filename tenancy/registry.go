package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrIdentityNotFound / ErrInviteNotFound / ErrUserInviteNotFound are returned when a lookup misses.
var (
	ErrIdentityNotFound   = errors.New("identity not found")
	ErrInviteNotFound     = errors.New("invite not found")
	ErrUserInviteNotFound = errors.New("user invite not found")
)

// Identity is a central login identity in the control plane.
type Identity struct {
	ID            string
	TenantID      string
	Email         string
	PasswordHash  string
	FullName      string
	EmailVerified bool
	SSOProvider   string
	SSOSubject    string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

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

// ListTenants returns all tenants ordered by creation time (platform admin view).
func (c *ControlPlane) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := c.pool.Query(ctx, "SELECT "+tenantColumns+" FROM tenants ORDER BY created_at DESC")
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

// ----- Identity writes/reads -------------------------------------------------

const identityColumns = `id, tenant_id, email, COALESCE(password_hash,''), full_name,
	email_verified, COALESCE(sso_provider,''), COALESCE(sso_subject,''), created_at, updated_at`

func scanIdentity(row pgx.Row) (*Identity, error) {
	var i Identity
	err := row.Scan(&i.ID, &i.TenantID, &i.Email, &i.PasswordHash, &i.FullName,
		&i.EmailVerified, &i.SSOProvider, &i.SSOSubject, &i.CreatedAt, &i.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrIdentityNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan identity: %w", err)
	}
	return &i, nil
}

// CreateIdentity inserts a central login identity for a tenant.
func (c *ControlPlane) CreateIdentity(ctx context.Context, tenantID, email, passwordHash, fullName string, emailVerified bool) (*Identity, error) {
	q := `INSERT INTO identities (tenant_id, email, password_hash, full_name, email_verified)
	      VALUES ($1, $2, $3, $4, $5)
	      RETURNING ` + identityColumns
	return scanIdentity(c.pool.QueryRow(ctx, q, tenantID, email, passwordHash, fullName, emailVerified))
}

// IdentityByEmail loads an identity by email (case-insensitive).
func (c *ControlPlane) IdentityByEmail(ctx context.Context, email string) (*Identity, error) {
	q := "SELECT " + identityColumns + " FROM identities WHERE LOWER(email) = LOWER($1)"
	return scanIdentity(c.pool.QueryRow(ctx, q, email))
}

// AnyIdentityForTenant returns the earliest-created identity for a tenant. Used
// to pick the first user to seed when provisioning the platform-owner workspace.
func (c *ControlPlane) AnyIdentityForTenant(ctx context.Context, tenantID string) (*Identity, error) {
	q := "SELECT " + identityColumns + " FROM identities WHERE tenant_id = $1 ORDER BY created_at LIMIT 1"
	return scanIdentity(c.pool.QueryRow(ctx, q, tenantID))
}

// SetIdentityPasswordSetupToken stores a one-time token + expiry an onboarded
// customer uses to set their initial password (reuses the password-reset cols).
func (c *ControlPlane) SetIdentityPasswordSetupToken(ctx context.Context, identityID, token string, expiry time.Time) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE identities SET password_reset_token = $2, password_reset_expiry = $3, updated_at = NOW() WHERE id = $1`,
		identityID, token, expiry); err != nil {
		return fmt.Errorf("set password setup token: %w", err)
	}
	return nil
}

// IdentityByPasswordToken loads an identity by its (unexpired) setup/reset token.
func (c *ControlPlane) IdentityByPasswordToken(ctx context.Context, token string) (*Identity, error) {
	q := "SELECT " + identityColumns + " FROM identities WHERE password_reset_token = $1 AND password_reset_expiry > NOW()"
	return scanIdentity(c.pool.QueryRow(ctx, q, token))
}

// SetIdentityPassword sets the password hash and clears the setup/reset token.
func (c *ControlPlane) SetIdentityPassword(ctx context.Context, identityID, passwordHash string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE identities SET password_hash = $2, password_reset_token = NULL, password_reset_expiry = NULL,
		 email_verified = TRUE, updated_at = NOW() WHERE id = $1`,
		identityID, passwordHash); err != nil {
		return fmt.Errorf("set identity password: %w", err)
	}
	return nil
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

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
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
