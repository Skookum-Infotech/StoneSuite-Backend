package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrSSOConfigNotFound is returned when an SSO config lookup misses (or the row
// belongs to a different tenant — the caller can never tell the two apart, so
// cross-tenant ids read as not-found).
var ErrSSOConfigNotFound = errors.New("sso config not found")

// SSOConfig is a per-tenant single-sign-on provider configuration. It is the
// read model: the client secret is write-only and never returned.
type SSOConfig struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Provider    string    `json:"provider"`
	ClientID    string    `json:"client_id"`
	Issuer      string    `json:"issuer"`
	RedirectURI string    `json:"redirect_uri"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SSOConfigInput carries the mutable fields of an SSO config. The client secret
// travels separately (already encrypted) so the store never touches the cipher.
type SSOConfigInput struct {
	Provider    string
	ClientID    string
	Issuer      string
	RedirectURI string
	Enabled     bool
}

const ssoConfigColumns = `id, tenant_id, provider, client_id, COALESCE(issuer, ''), COALESCE(redirect_uri, ''), enabled, created_at, updated_at`

func scanSSOConfig(row pgx.Row) (*SSOConfig, error) {
	var c SSOConfig
	if err := row.Scan(&c.ID, &c.TenantID, &c.Provider, &c.ClientID, &c.Issuer,
		&c.RedirectURI, &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListSSOConfigs returns every SSO config for a tenant, newest first.
func (c *ControlPlane) ListSSOConfigs(ctx context.Context, tenantID string) ([]SSOConfig, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT `+ssoConfigColumns+`
		FROM tenant_sso_configs
		WHERE tenant_id = $1
		ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list sso configs: %w", err)
	}
	defer rows.Close()

	configs := make([]SSOConfig, 0)
	for rows.Next() {
		cfg, err := scanSSOConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sso config: %w", err)
		}
		configs = append(configs, *cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sso configs: %w", err)
	}
	return configs, nil
}

// GetSSOConfig loads one SSO config scoped to the tenant. A row belonging to a
// different tenant reads as ErrSSOConfigNotFound (IDOR guard by construction).
func (c *ControlPlane) GetSSOConfig(ctx context.Context, tenantID, id string) (*SSOConfig, error) {
	cfg, err := scanSSOConfig(c.pool.QueryRow(ctx, `
		SELECT `+ssoConfigColumns+`
		FROM tenant_sso_configs
		WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSSOConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get sso config: %w", err)
	}
	return cfg, nil
}

// CreateSSOConfig inserts a new SSO config. encSecret must already be encrypted.
// A duplicate (tenant_id, provider) surfaces as a unique-violation error the
// caller maps to 409.
func (c *ControlPlane) CreateSSOConfig(ctx context.Context, tenantID string, in SSOConfigInput, encSecret string) (*SSOConfig, error) {
	cfg, err := scanSSOConfig(c.pool.QueryRow(ctx, `
		INSERT INTO tenant_sso_configs (tenant_id, provider, client_id, client_secret_enc, issuer, redirect_uri, enabled)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7)
		RETURNING `+ssoConfigColumns,
		tenantID, in.Provider, in.ClientID, encSecret, in.Issuer, in.RedirectURI, in.Enabled))
	if err != nil {
		return nil, fmt.Errorf("create sso config: %w", err)
	}
	return cfg, nil
}

// UpdateSSOConfig updates an SSO config scoped to the tenant. When encSecret is
// nil the stored client secret is left untouched; otherwise it is replaced.
// Missing (or cross-tenant) rows return ErrSSOConfigNotFound.
func (c *ControlPlane) UpdateSSOConfig(ctx context.Context, tenantID, id string, in SSOConfigInput, encSecret *string) (*SSOConfig, error) {
	cfg, err := scanSSOConfig(c.pool.QueryRow(ctx, `
		UPDATE tenant_sso_configs
		SET provider          = $3,
		    client_id         = $4,
		    client_secret_enc = COALESCE($5, client_secret_enc),
		    issuer            = NULLIF($6, ''),
		    redirect_uri      = NULLIF($7, ''),
		    enabled           = $8,
		    updated_at        = NOW()
		WHERE tenant_id = $1 AND id = $2
		RETURNING `+ssoConfigColumns,
		tenantID, id, in.Provider, in.ClientID, encSecret, in.Issuer, in.RedirectURI, in.Enabled))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSSOConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update sso config: %w", err)
	}
	return cfg, nil
}

// DeleteSSOConfig removes an SSO config scoped to the tenant. Missing (or
// cross-tenant) rows return ErrSSOConfigNotFound.
func (c *ControlPlane) DeleteSSOConfig(ctx context.Context, tenantID, id string) error {
	tag, err := c.pool.Exec(ctx, `
		DELETE FROM tenant_sso_configs
		WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return fmt.Errorf("delete sso config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSSOConfigNotFound
	}
	return nil
}
