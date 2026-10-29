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
	ID              string
	TenantID        string
	Email           string
	PasswordHash    string
	FullName        string
	EmailVerified   bool
	SSOProvider     string
	SSOSubject      string
	SSOSessionIndex string
	MFAEnabled      bool // emailed sign-in code step at password login
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ----- Identity writes/reads -------------------------------------------------

const identityColumns = `id, tenant_id, email, COALESCE(password_hash,''), full_name,
	email_verified, COALESCE(sso_provider,''), COALESCE(sso_subject,''), COALESCE(sso_session_index,''),
	mfa_enabled, created_at, updated_at`

func scanIdentity(row pgx.Row) (*Identity, error) {
	var i Identity
	err := row.Scan(&i.ID, &i.TenantID, &i.Email, &i.PasswordHash, &i.FullName,
		&i.EmailVerified, &i.SSOProvider, &i.SSOSubject, &i.SSOSessionIndex, &i.MFAEnabled, &i.CreatedAt, &i.UpdatedAt)
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
// Only HashToken(token) is persisted.
func (c *ControlPlane) SetIdentityPasswordSetupToken(ctx context.Context, identityID, token string, expiry time.Time) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE identities SET password_reset_token = $2, password_reset_expiry = $3, updated_at = NOW() WHERE id = $1`,
		identityID, HashToken(token), expiry); err != nil {
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
	return scanIdentity(c.pool.QueryRow(ctx, q, HashToken(token)))
}

// ConsumePasswordToken sets the password and burns the setup/reset token in
// one statement, matched on the token still being present and unexpired. A
// lookup followed by an unconditional update let two concurrent requests both
// spend the same link; here only one row change wins and the other gets
// ErrTokenAlreadyUsed. In the same transaction it revokes every refresh token
// the identity holds: whoever forced a reset may be holding one of them.
// rawToken is what the link carried.
func (c *ControlPlane) ConsumePasswordToken(ctx context.Context, identityID, rawToken, passwordHash string) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("consume password token: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit
	tag, err := tx.Exec(ctx,
		`UPDATE identities SET password_hash = $2, password_reset_token = NULL, password_reset_expiry = NULL,
		 email_verified = TRUE, updated_at = NOW()
		 WHERE id = $1 AND password_reset_token = $3 AND password_reset_expiry > NOW()`,
		identityID, passwordHash, HashToken(rawToken))
	if err != nil {
		return fmt.Errorf("consume password token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTokenAlreadyUsed
	}
	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = NOW() WHERE identity_id = $1 AND revoked_at IS NULL`,
		identityID); err != nil {
		return fmt.Errorf("consume password token: revoke sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("consume password token: commit: %w", err)
	}
	return nil
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
	defer c.forgetTenant(tenantID)
	if _, err := c.pool.Exec(ctx,
		`UPDATE tenants SET status = 'active', updated_at = NOW() WHERE id = $1`,
		tenantID); err != nil {
		return fmt.Errorf("activate platform owner: %w", err)
	}
	return nil
}

// setIdentityPasswordSQL sets the password hash, clears the setup/reset token
// and marks the email verified.
const setIdentityPasswordSQL = `UPDATE identities SET password_hash = $2, password_reset_token = NULL,
	password_reset_expiry = NULL, email_verified = TRUE, updated_at = NOW() WHERE id = $1`

// SetIdentityPassword sets the password hash and clears the setup/reset token.
func (c *ControlPlane) SetIdentityPassword(ctx context.Context, identityID, passwordHash string) error {
	if _, err := c.pool.Exec(ctx,
		setIdentityPasswordSQL,
		identityID, passwordHash); err != nil {
		return fmt.Errorf("set identity password: %w", err)
	}
	return nil
}

// SetIdentityMFAEnabled turns the emailed sign-in code step on or off.
// mfa_enabled_at records when it was last turned on and is cleared on off.
func (c *ControlPlane) SetIdentityMFAEnabled(ctx context.Context, identityID string, enabled bool) error {
	tag, err := c.pool.Exec(ctx,
		`UPDATE identities
		 SET mfa_enabled = $2,
		     mfa_enabled_at = CASE WHEN $2 THEN NOW() ELSE NULL END,
		     updated_at = NOW()
		 WHERE id = $1`,
		identityID, enabled)
	if err != nil {
		return fmt.Errorf("set identity mfa enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrIdentityNotFound
	}
	return nil
}

// SetIdentityPasswordRevokingSessions is SetIdentityPassword for a password
// change or reset: in the same transaction it revokes every refresh token the
// identity holds, so a session opened with the old password (or a stolen
// refresh token) cannot outlive the change. keepTokenHash, when non-empty,
// spares that one token of this identity — the session that just proved the
// current password. Reset passes "": the person resetting is not signed in.
func (c *ControlPlane) SetIdentityPasswordRevokingSessions(ctx context.Context, identityID, passwordHash, keepTokenHash string) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set identity password: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit
	if _, err := tx.Exec(ctx,
		setIdentityPasswordSQL,
		identityID, passwordHash); err != nil {
		return fmt.Errorf("set identity password: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = NOW()
		 WHERE identity_id = $1 AND revoked_at IS NULL AND token_hash <> $2`,
		identityID, keepTokenHash); err != nil {
		return fmt.Errorf("set identity password: revoke sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("set identity password: commit: %w", err)
	}
	return nil
}

// MFAState is one identity's two-step setting as an administrator sees it.
type MFAState struct {
	Enabled bool
	// Available is false for identities with no password step to add a code
	// to (single sign-on only).
	Available bool
}

// IdentityMFAStates loads the two-step setting of the given identities in one
// query, keyed by identity id. Scoped to tenantID, so an id from another
// workspace is simply absent from the result.
func (c *ControlPlane) IdentityMFAStates(ctx context.Context, tenantID string, identityIDs []string) (map[string]MFAState, error) {
	states := make(map[string]MFAState, len(identityIDs))
	if len(identityIDs) == 0 {
		return states, nil
	}
	rows, err := c.pool.Query(ctx,
		`SELECT id::text, mfa_enabled, COALESCE(password_hash, '') <> ''
		 FROM identities
		 WHERE tenant_id = $1 AND id::text = ANY($2)`,
		tenantID, identityIDs)
	if err != nil {
		return nil, fmt.Errorf("identity mfa states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var s MFAState
		if err := rows.Scan(&id, &s.Enabled, &s.Available); err != nil {
			return nil, fmt.Errorf("scan identity mfa state: %w", err)
		}
		states[id] = s
	}
	return states, rows.Err()
}

// CreateSSOIdentity inserts a central login identity created via SAML/SSO
// first-login. Unlike CreateIdentity (password path), there is no password
// hash, and email_verified is always true -- the identity provider already
// vouched for the address.
func (c *ControlPlane) CreateSSOIdentity(ctx context.Context, tenantID, email, fullName, ssoProvider, ssoSubject string) (*Identity, error) {
	q := `INSERT INTO identities (tenant_id, email, full_name, email_verified, sso_provider, sso_subject)
	      VALUES ($1, $2, $3, TRUE, $4, $5)
	      RETURNING ` + identityColumns
	return scanIdentity(c.pool.QueryRow(ctx, q, tenantID, email, fullName, ssoProvider, ssoSubject))
}

// LinkSSOIdentity sets sso_provider/sso_subject/sso_session_index on an
// existing identity. Idempotent: safe to call on every successful SAML
// login, including repeat logins where the values are unchanged. Overwrites
// any prior (provider, subject, session index) tuple -- the caller
// (controllers layer) is responsible for auditing changes it considers
// noteworthy (e.g. a subject that changed since last login) before calling
// this; this method just performs the write.
func (c *ControlPlane) LinkSSOIdentity(ctx context.Context, identityID, ssoProvider, ssoSubject, sessionIndex string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE identities SET sso_provider = $2, sso_subject = $3, sso_session_index = $4, updated_at = NOW() WHERE id = $1`,
		identityID, ssoProvider, ssoSubject, sessionIndex); err != nil {
		return fmt.Errorf("link sso identity: %w", err)
	}
	return nil
}
