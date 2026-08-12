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

// defaultSSOProtocol is tenant_sso_configs.protocol's DB-level default,
// applied here too wherever the column is written explicitly (an explicit
// INSERT/UPDATE column list bypasses the DB DEFAULT, which only applies when
// a column is omitted outright).
const defaultSSOProtocol = "oidc"

// ssoProtocolSAML is the tenant_sso_configs.protocol value identifying a SAML
// config (the other value the protocol_check constraint allows is
// defaultSSOProtocol, "oidc").
const ssoProtocolSAML = "saml"

// defaultSAMLNameIDFormat mirrors tenant_sso_configs.name_id_format's DB-level
// default. Applied here for the same reason as defaultSSOProtocol above.
const defaultSAMLNameIDFormat = "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"

// SSOConfig is a per-tenant single-sign-on provider configuration. It is the
// read model: the client secret and IdP certificate are write-only and never
// returned.
type SSOConfig struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	Provider    string `json:"provider"`
	Protocol    string `json:"protocol"` // "oidc" or "saml"; NOT omitempty -- always present, defaults to "oidc" at the DB level
	ClientID    string `json:"client_id,omitempty"`
	Issuer      string `json:"issuer,omitempty"`
	RedirectURI string `json:"redirect_uri,omitempty"`
	// SAML-specific (empty/omitted for protocol="oidc" configs)
	MetadataURL            string     `json:"metadata_url,omitempty"`
	IDPEntityID            string     `json:"idp_entity_id,omitempty"`
	SSOURL                 string     `json:"sso_url,omitempty"`
	SLOURL                 string     `json:"slo_url,omitempty"`
	CertificateFingerprint string     `json:"certificate_fingerprint,omitempty"` // fingerprint only -- the certificate PEM itself is never returned, same write-only discipline as client_secret
	NameIDFormat           string     `json:"name_id_format,omitempty"`
	MetadataFetchedAt      *time.Time `json:"metadata_fetched_at,omitempty"`
	Enabled                bool       `json:"enabled"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// SSOConfigInput carries the mutable fields of an SSO config. The client
// secret and IdP certificate travel separately (already encrypted) so the
// store never touches the cipher; CertificateFingerprint and
// MetadataFetchedAt are store-computed and travel as explicit method params.
type SSOConfigInput struct {
	Provider     string
	Protocol     string
	ClientID     string
	Issuer       string
	RedirectURI  string
	MetadataURL  string
	IDPEntityID  string
	SSOURL       string
	SLOURL       string
	NameIDFormat string
	Enabled      bool
}

const ssoConfigColumns = `id, tenant_id, provider, client_id, COALESCE(issuer, ''), COALESCE(redirect_uri, ''), enabled, created_at, updated_at,
	protocol, COALESCE(metadata_url, ''), COALESCE(idp_entity_id, ''), COALESCE(sso_url, ''), COALESCE(slo_url, ''),
	COALESCE(certificate_fingerprint, ''), name_id_format, metadata_fetched_at`

func scanSSOConfig(row pgx.Row) (*SSOConfig, error) {
	var c SSOConfig
	if err := row.Scan(&c.ID, &c.TenantID, &c.Provider, &c.ClientID, &c.Issuer,
		&c.RedirectURI, &c.Enabled, &c.CreatedAt, &c.UpdatedAt,
		&c.Protocol, &c.MetadataURL, &c.IDPEntityID, &c.SSOURL, &c.SLOURL,
		&c.CertificateFingerprint, &c.NameIDFormat, &c.MetadataFetchedAt); err != nil {
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

// CreateSSOConfig inserts a new SSO config. encSecret must already be
// encrypted (empty string for protocol="saml", which has no OAuth client
// secret). encCertPEM must already be encrypted (empty string for
// protocol="oidc"). certFingerprint is plaintext (SHA-256 hex, not
// sensitive -- it's a public value derivable from the IdP's own published
// metadata, used only for admin-facing display/audit, never for trust
// decisions). A duplicate (tenant_id, provider) surfaces as a
// unique-violation error the caller maps to 409.
func (c *ControlPlane) CreateSSOConfig(ctx context.Context, tenantID string, in SSOConfigInput, encSecret, encCertPEM, certFingerprint string) (*SSOConfig, error) {
	// A fresh cert or metadata URL means the caller just fetched IdP metadata;
	// stamp the fetch time. An OIDC config (neither set) gets no timestamp.
	var metadataFetchedAt *time.Time
	if encCertPEM != "" || in.MetadataURL != "" {
		now := time.Now()
		metadataFetchedAt = &now
	}

	cfg, err := scanSSOConfig(c.pool.QueryRow(ctx, `
		INSERT INTO tenant_sso_configs (
			tenant_id, provider, client_id, client_secret_enc, issuer, redirect_uri, enabled,
			protocol, metadata_url, idp_entity_id, sso_url, slo_url,
			certificate_pem_enc, certificate_fingerprint, name_id_format, metadata_fetched_at
		)
		VALUES (
			$1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7,
			COALESCE(NULLIF($8, ''), '`+defaultSSOProtocol+`'), NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, ''),
			NULLIF($13, ''), NULLIF($14, ''), COALESCE(NULLIF($15, ''), '`+defaultSAMLNameIDFormat+`'), $16
		)
		RETURNING `+ssoConfigColumns,
		tenantID, in.Provider, in.ClientID, encSecret, in.Issuer, in.RedirectURI, in.Enabled,
		in.Protocol, in.MetadataURL, in.IDPEntityID, in.SSOURL, in.SLOURL,
		encCertPEM, certFingerprint, in.NameIDFormat, metadataFetchedAt))
	if err != nil {
		return nil, fmt.Errorf("create sso config: %w", err)
	}
	return cfg, nil
}

// UpdateSSOConfig updates an SSO config scoped to the tenant. When encSecret,
// encCertPEM, or certFingerprint is nil, the corresponding stored value is
// left untouched; otherwise it is replaced. metadataFetchedAt is written only
// when non-nil (set it to the fetch time when the caller just refreshed IdP
// metadata; leave nil otherwise). Missing (or cross-tenant) rows return
// ErrSSOConfigNotFound.
func (c *ControlPlane) UpdateSSOConfig(ctx context.Context, tenantID, id string, in SSOConfigInput, encSecret, encCertPEM, certFingerprint *string, metadataFetchedAt *time.Time) (*SSOConfig, error) {
	cfg, err := scanSSOConfig(c.pool.QueryRow(ctx, `
		UPDATE tenant_sso_configs
		SET provider                = $3,
		    client_id               = $4,
		    client_secret_enc       = COALESCE($5, client_secret_enc),
		    issuer                  = NULLIF($6, ''),
		    redirect_uri            = NULLIF($7, ''),
		    enabled                 = $8,
		    protocol                = COALESCE(NULLIF($9, ''), '`+defaultSSOProtocol+`'),
		    metadata_url            = NULLIF($10, ''),
		    idp_entity_id           = NULLIF($11, ''),
		    sso_url                 = NULLIF($12, ''),
		    slo_url                 = NULLIF($13, ''),
		    certificate_pem_enc     = COALESCE($14, certificate_pem_enc),
		    certificate_fingerprint = COALESCE($15, certificate_fingerprint),
		    name_id_format          = COALESCE(NULLIF($16, ''), '`+defaultSAMLNameIDFormat+`'),
		    metadata_fetched_at     = COALESCE($17, metadata_fetched_at),
		    updated_at              = NOW()
		WHERE tenant_id = $1 AND id = $2
		RETURNING `+ssoConfigColumns,
		tenantID, id, in.Provider, in.ClientID, encSecret, in.Issuer, in.RedirectURI, in.Enabled,
		in.Protocol, in.MetadataURL, in.IDPEntityID, in.SSOURL, in.SLOURL,
		encCertPEM, certFingerprint, in.NameIDFormat, metadataFetchedAt))
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

// ============================================================================
// SAML auth flow (internal only -- never JSON-marshaled to an HTTP response)
// ============================================================================

// SSOConfigAuth is the full SAML operational record needed to run an
// authentication flow, including the still-encrypted certificate. Unlike
// SSOConfig (the API read model), this is never serialized to JSON --
// callers must decrypt CertificatePEMEnc via the secret cipher before use
// and must never write this struct to an http.ResponseWriter.
type SSOConfigAuth struct {
	ID                string
	TenantID          string
	Provider          string
	Protocol          string
	IDPEntityID       string
	SSOURL            string
	SLOURL            string
	CertificatePEMEnc string
	NameIDFormat      string
	Enabled           bool
}

// GetSSOConfigForAuth loads the enabled SAML config for a tenant+provider,
// for use by the SAML login/ACS/logout handlers. Returns ErrSSOConfigNotFound
// if no matching, enabled, protocol='saml' config exists.
func (c *ControlPlane) GetSSOConfigForAuth(ctx context.Context, tenantID, provider string) (*SSOConfigAuth, error) {
	var a SSOConfigAuth
	err := c.pool.QueryRow(ctx, `
		SELECT id, tenant_id, provider, protocol, COALESCE(idp_entity_id, ''), COALESCE(sso_url, ''),
		       COALESCE(slo_url, ''), COALESCE(certificate_pem_enc, ''), name_id_format, enabled
		FROM tenant_sso_configs
		WHERE tenant_id = $1 AND provider = $2 AND protocol = '`+ssoProtocolSAML+`' AND enabled = TRUE`,
		tenantID, provider,
	).Scan(&a.ID, &a.TenantID, &a.Provider, &a.Protocol, &a.IDPEntityID, &a.SSOURL,
		&a.SLOURL, &a.CertificatePEMEnc, &a.NameIDFormat, &a.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSSOConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get sso config for auth: %w", err)
	}
	return &a, nil
}
