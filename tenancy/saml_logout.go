package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// maxSSOConfigsPerIssuer bounds how many tenants' configs one IdP issuer can
// resolve to. In practice an IdP tenant maps to one StoneSuite tenant; the cap
// keeps a pathological row count from turning one request into many signature
// checks.
const maxSSOConfigsPerIssuer = 10

// ErrSSOIdentityNotFound means no identity in the tenant signed in through the
// provider with that subject (NameID).
var ErrSSOIdentityNotFound = errors.New("sso identity not found")

// SSOConfigsByIssuer returns the enabled SAML configs of a provider whose IdP
// entity id is idpEntityID. An IdP-initiated LogoutRequest names only its
// issuer, not a tenant, so the issuer is the one handle there is: the caller
// must still prove the request with each config's certificate before trusting
// which tenant it belongs to.
func (c *ControlPlane) SSOConfigsByIssuer(ctx context.Context, provider, idpEntityID string) ([]SSOConfigAuth, error) {
	if idpEntityID == "" {
		return nil, nil
	}
	rows, err := c.pool.Query(ctx, `
		SELECT id, tenant_id, provider, protocol, COALESCE(idp_entity_id, ''), COALESCE(sso_url, ''),
		       COALESCE(slo_url, ''), COALESCE(certificate_pem_enc, ''), name_id_format,
		       COALESCE(default_role_id::text, ''), enabled
		FROM tenant_sso_configs
		WHERE provider = $1 AND idp_entity_id = $2 AND protocol = '`+ssoProtocolSAML+`' AND enabled = TRUE
		ORDER BY created_at
		LIMIT $3`, provider, idpEntityID, maxSSOConfigsPerIssuer)
	if err != nil {
		return nil, fmt.Errorf("sso configs by issuer: %w", err)
	}
	defer rows.Close()
	var out []SSOConfigAuth
	for rows.Next() {
		var a SSOConfigAuth
		if err := rows.Scan(&a.ID, &a.TenantID, &a.Provider, &a.Protocol, &a.IDPEntityID, &a.SSOURL,
			&a.SLOURL, &a.CertificatePEMEnc, &a.NameIDFormat, &a.DefaultRoleID, &a.Enabled); err != nil {
			return nil, fmt.Errorf("sso configs by issuer: scan: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sso configs by issuer: %w", err)
	}
	return out, nil
}

// IdentityBySSOSubject loads the identity in tenantID that signed in through
// provider with this subject (the SAML NameID). Scoping by tenant is the
// point: a NameID proven by one tenant's IdP must never reach another
// tenant's identity.
func (c *ControlPlane) IdentityBySSOSubject(ctx context.Context, tenantID, provider, subject string) (*Identity, error) {
	if subject == "" {
		return nil, ErrSSOIdentityNotFound
	}
	q := "SELECT " + identityColumns + ` FROM identities
		WHERE tenant_id = $1 AND sso_provider = $2 AND sso_subject = $3`
	id, err := scanIdentity(c.pool.QueryRow(ctx, q, tenantID, provider, subject))
	// scanIdentity has already turned "no rows" into ErrIdentityNotFound.
	if errors.Is(err, ErrIdentityNotFound) {
		return nil, ErrSSOIdentityNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("identity by sso subject: %w", err)
	}
	return id, nil
}

// ClaimSAMLLogoutRequest records that the IdP's LogoutRequest (issuer, id) is
// being acted on, and reports whether this is the first time. A false result
// is a replay and must not be acted on. The row is kept until expiresAt, the
// end of the window in which the request would be accepted anyway; expired
// rows are pruned on the way in.
func (c *ControlPlane) ClaimSAMLLogoutRequest(ctx context.Context, issuer, requestID string, expiresAt time.Time) (bool, error) {
	if _, err := c.pool.Exec(ctx, `DELETE FROM saml_logout_requests WHERE expires_at < NOW()`); err != nil {
		return false, fmt.Errorf("prune saml logout requests: %w", err)
	}
	tag, err := c.pool.Exec(ctx, `
		INSERT INTO saml_logout_requests (issuer, request_id, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (issuer, request_id) DO NOTHING`, issuer, requestID, expiresAt)
	if err != nil {
		return false, fmt.Errorf("claim saml logout request: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
