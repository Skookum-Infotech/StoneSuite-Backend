package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrIdentityNotFound is returned when an identity lookup misses.
var ErrIdentityNotFound = errors.New("identity not found")

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

// IdentityByID loads an identity by its UUID.
func (c *ControlPlane) IdentityByID(ctx context.Context, id string) (*Identity, error) {
	q := "SELECT " + identityColumns + " FROM identities WHERE id = $1"
	return scanIdentity(c.pool.QueryRow(ctx, q, id))
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

// SetIdentitySetupTokenHash stores SHA-256(raw_token) as the activation token.
// The raw token is printed to server stdout and never persisted in the database.
func (c *ControlPlane) SetIdentitySetupTokenHash(ctx context.Context, identityID, tokenHash string, expiry time.Time) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE identities SET password_reset_token = $2, password_reset_expiry = $3, updated_at = NOW() WHERE id = $1`,
		identityID, tokenHash, expiry); err != nil {
		return fmt.Errorf("set setup token hash: %w", err)
	}
	return nil
}

// IdentityByPasswordToken loads an identity by its (unexpired) setup/reset token.
func (c *ControlPlane) IdentityByPasswordToken(ctx context.Context, token string) (*Identity, error) {
	q := "SELECT " + identityColumns + " FROM identities WHERE password_reset_token = $1 AND password_reset_expiry > NOW()"
	return scanIdentity(c.pool.QueryRow(ctx, q, token))
}

// IdentityBySetupTokenHash loads an identity by SHA-256(raw_token) for the
// platform activation flow. Token must not be expired.
func (c *ControlPlane) IdentityBySetupTokenHash(ctx context.Context, tokenHash string) (*Identity, error) {
	q := "SELECT " + identityColumns + " FROM identities WHERE password_reset_token = $1 AND password_reset_expiry > NOW()"
	return scanIdentity(c.pool.QueryRow(ctx, q, tokenHash))
}

// ActivatePlatformOwner marks the platform-owner tenant active. Called once
// the admin successfully activates their account via the setup token.
func (c *ControlPlane) ActivatePlatformOwner(ctx context.Context, tenantID string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE tenants SET status = 'active', updated_at = NOW() WHERE id = $1`,
		tenantID); err != nil {
		return fmt.Errorf("activate platform owner: %w", err)
	}
	return nil
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
