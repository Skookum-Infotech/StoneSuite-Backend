package tenancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrSSODomainNotFound is returned when a domain lookup misses (or the row
// belongs to a different tenant — the caller can never tell the two apart, so
// cross-tenant ids read as not-found).
var ErrSSODomainNotFound = errors.New("sso domain not found")

// SSODomain is an email domain registered against a tenant's SSO config, used
// for home-realm discovery at sign-in.
type SSODomain struct {
	ID          string     `json:"id"`
	SSOConfigID string     `json:"sso_config_id"`
	Domain      string     `json:"domain"`
	VerifiedAt  *time.Time `json:"verified_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// NormalizeEmailDomain lowercases and trims a domain, stripping a leading "@"
// and anything before it so both "contoso.com" and "user@contoso.com" reduce
// to the same key. Returns "" when nothing usable remains.
func NormalizeEmailDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if at := strings.LastIndex(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	return strings.TrimSpace(s)
}

// ListSSODomains returns the domains registered against one SSO config,
// scoped to the tenant that owns the config (cross-tenant config ids yield an
// empty list rather than another tenant's domains).
func (c *ControlPlane) ListSSODomains(ctx context.Context, tenantID, ssoConfigID string) ([]SSODomain, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT d.id, d.sso_config_id, d.domain, d.verified_at, d.created_at
		FROM tenant_sso_domains d
		JOIN tenant_sso_configs cfg ON cfg.id = d.sso_config_id
		WHERE d.sso_config_id = $1 AND cfg.tenant_id = $2
		ORDER BY d.domain`, ssoConfigID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list sso domains: %w", err)
	}
	defer rows.Close()

	domains := make([]SSODomain, 0)
	for rows.Next() {
		var d SSODomain
		if err := rows.Scan(&d.ID, &d.SSOConfigID, &d.Domain, &d.VerifiedAt, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan sso domain: %w", err)
		}
		domains = append(domains, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sso domains: %w", err)
	}
	return domains, nil
}

// CreateSSODomain registers a domain against an SSO config the tenant owns.
// The config ownership check is folded into the INSERT's SELECT so a
// cross-tenant sso_config_id inserts nothing and reads as
// ErrSSOConfigNotFound. A domain already claimed by any tenant surfaces as a
// unique-violation the caller maps to 409.
func (c *ControlPlane) CreateSSODomain(ctx context.Context, tenantID, ssoConfigID, domain string) (*SSODomain, error) {
	var d SSODomain
	err := c.pool.QueryRow(ctx, `
		INSERT INTO tenant_sso_domains (sso_config_id, domain)
		SELECT cfg.id, $3
		FROM tenant_sso_configs cfg
		WHERE cfg.id = $1 AND cfg.tenant_id = $2
		RETURNING id, sso_config_id, domain, verified_at, created_at`,
		ssoConfigID, tenantID, domain,
	).Scan(&d.ID, &d.SSOConfigID, &d.Domain, &d.VerifiedAt, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// The SELECT matched no config -- either it doesn't exist or it
		// belongs to another tenant. Indistinguishable by design.
		return nil, ErrSSOConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("create sso domain: %w", err)
	}
	return &d, nil
}

// DeleteSSODomain removes a registered domain, scoped to the tenant that owns
// the parent config. Missing (or cross-tenant) rows return
// ErrSSODomainNotFound.
func (c *ControlPlane) DeleteSSODomain(ctx context.Context, tenantID, domainID string) error {
	tag, err := c.pool.Exec(ctx, `
		DELETE FROM tenant_sso_domains d
		USING tenant_sso_configs cfg
		WHERE d.id = $1 AND d.sso_config_id = cfg.id AND cfg.tenant_id = $2`,
		domainID, tenantID)
	if err != nil {
		return fmt.Errorf("delete sso domain: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSSODomainNotFound
	}
	return nil
}

// SSODiscovery is the result of resolving an email domain to a tenant and
// SAML provider for home-realm discovery.
type SSODiscovery struct {
	TenantID string
	Provider string
}

// DiscoverSSOByEmailDomain resolves a normalized email domain to its tenant +
// provider. Mirrors GetSSOConfigForAuth's guards exactly -- only an enabled,
// protocol='saml' config matches -- so a disabled or OIDC-only config is
// indistinguishable from an unregistered domain. Returns ErrSSOConfigNotFound
// when nothing matches; the caller is responsible for also confirming the
// tenant is servable before acting on the result.
func (c *ControlPlane) DiscoverSSOByEmailDomain(ctx context.Context, domain string) (*SSODiscovery, error) {
	var d SSODiscovery
	err := c.pool.QueryRow(ctx, `
		SELECT cfg.tenant_id, cfg.provider
		FROM tenant_sso_domains dom
		JOIN tenant_sso_configs cfg ON cfg.id = dom.sso_config_id
		WHERE LOWER(dom.domain) = $1
		  AND cfg.protocol = '`+ssoProtocolSAML+`'
		  AND cfg.enabled = TRUE`,
		domain,
	).Scan(&d.TenantID, &d.Provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSSOConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("discover sso by email domain: %w", err)
	}
	return &d, nil
}
