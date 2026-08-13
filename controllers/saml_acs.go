package controllers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	saml2 "github.com/russellhaering/gosaml2"

	"stonesuite-backend/authz"
	"stonesuite-backend/saml"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// samlLoginCodeTTL is how long a login code minted by ACS remains
// exchangeable (POST /api/auth/saml/exchange) before it expires --
// deliberately short since the frontend redirect-and-exchange round trip
// happens within the same browser navigation.
const samlLoginCodeTTL = 60 * time.Second

// samlErrorPageHTML is a minimal, fully static error page. It never
// interpolates any request- or assertion-derived content -- every ACS
// failure's detail goes to the server-side security log instead, never to
// the browser.
const samlErrorPageHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Sign-in failed</title></head>
<body>
<h1>Sign-in failed</h1>
<p>Please try again or contact your administrator.</p>
</body>
</html>`

// samlErrorPage renders the static SAML sign-in failure page. Callers log
// full failure detail server-side via logSecurityEvent before calling this.
func samlErrorPage(w http.ResponseWriter, statusCode int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(samlErrorPageHTML))
}

// nameAttributeCandidates lists single-attribute SAML claim names checked, in
// order, for a display name before falling back to given+surname or the
// email's local part.
var nameAttributeCandidates = []string{
	"name",
	"displayName",
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name",
}

// givenNameAttribute and surnameAttribute are combined (space-joined) as a
// display-name fallback when no single full-name attribute is present.
const (
	givenNameAttribute = "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/givenname"
	surnameAttribute   = "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/surname"
)

// bestEffortNameFromAssertion derives a display name for a JIT-provisioned
// user from a validated SAML assertion: a single full-name attribute if
// present, else given+surname joined, else the local part of the email
// (everything before '@'), else the raw email as a last resort.
func bestEffortNameFromAssertion(assertion *saml.ParsedAssertion) string {
	for _, candidate := range nameAttributeCandidates {
		if v := strings.TrimSpace(assertion.Attributes[candidate]); v != "" {
			return v
		}
	}
	given := strings.TrimSpace(assertion.Attributes[givenNameAttribute])
	surname := strings.TrimSpace(assertion.Attributes[surnameAttribute])
	if full := strings.TrimSpace(given + " " + surname); full != "" {
		return full
	}
	if local, _, found := strings.Cut(assertion.Email, "@"); found && local != "" {
		return local
	}
	return assertion.Email
}

// ACS is the SAML Assertion Consumer Service: the IdP POSTs its SAMLResponse
// here after authenticating the user. Every failure renders a generic error
// page; full detail goes only to the server-side security log. Tenant
// resolution is via the response's InResponseTo correlated against
// server-generated, single-use saml_requests state -- never from anything
// client-supplied. Path: POST /api/auth/saml/{provider}/acs
func (h *SAMLAuthOps) ACS(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if !isValidSAMLProvider(provider) {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		logSecurityEvent(r, "saml_acs_missing_response", "provider", provider, "error", err.Error())
		samlErrorPage(w, http.StatusBadRequest)
		return
	}
	samlResponse := r.FormValue("SAMLResponse")
	if samlResponse == "" {
		logSecurityEvent(r, "saml_acs_missing_response", "provider", provider)
		samlErrorPage(w, http.StatusBadRequest)
		return
	}

	// Step 1: decode UNVERIFIED to learn only InResponseTo -- an opaque,
	// server-generated, single-use correlation id. Nothing else from this
	// unverified struct is trusted for any security decision below.
	unverified, decErr := saml2.DecodeUnverifiedBaseResponse(samlResponse)
	if decErr != nil || unverified == nil || unverified.InResponseTo == "" {
		reason := "missing InResponseTo"
		if decErr != nil {
			reason = decErr.Error()
		}
		logSecurityEvent(r, "saml_acs_malformed_response", "provider", provider, "error", reason)
		samlErrorPage(w, http.StatusBadRequest)
		return
	}

	// Step 2: resolve the tenant via that correlation id, consuming it
	// atomically (single-use). Only after this do we know which tenant's
	// certificate to validate the signature against.
	tenantID, err := h.cp.ConsumeSAMLRequestState(ctx, unverified.InResponseTo, provider)
	if err != nil {
		logSecurityEvent(r, "saml_request_state_invalid", "provider", provider, "error", err.Error())
		samlErrorPage(w, http.StatusBadRequest)
		return
	}

	tenant, err := h.cp.TenantByID(ctx, tenantID)
	if err != nil {
		logSecurityEvent(r, "saml_acs_tenant_unservable", "provider", provider, "tenant_id", tenantID, "error", err.Error())
		samlErrorPage(w, http.StatusInternalServerError)
		return
	}
	if !tenant.Servable() {
		logSecurityEvent(r, "saml_acs_tenant_unservable", "provider", provider, "tenant_id", tenantID, "status", tenant.Status)
		samlErrorPage(w, http.StatusForbidden)
		return
	}

	cfg, err := h.cp.GetSSOConfigForAuth(ctx, tenantID, provider)
	if err != nil {
		logSecurityEvent(r, "saml_acs_config_not_found", "provider", provider, "tenant_id", tenantID, "error", err.Error())
		samlErrorPage(w, http.StatusBadRequest)
		return
	}

	if h.cipher == nil {
		// Defensively unreachable -- a SAML config can't exist without the
		// cipher having been available at create time -- but never trust
		// that invariant blindly.
		logSecurityEvent(r, "saml_acs_no_cipher", "provider", provider, "tenant_id", tenantID)
		samlErrorPage(w, http.StatusInternalServerError)
		return
	}
	certPEM, err := h.cipher.Decrypt(cfg.CertificatePEMEnc)
	if err != nil {
		logSecurityEvent(r, "saml_acs_cert_decrypt_failed", "provider", provider, "tenant_id", tenantID, "error", err.Error())
		samlErrorPage(w, http.StatusInternalServerError)
		return
	}

	// Step 3: full signature validation, now that we know which cert to
	// check against. This is the actual security boundary.
	spEntityID, acsURL, sloURL := spConfig(provider)
	samlCfg := saml.Config{
		SPEntityID:   spEntityID,
		ACSURL:       acsURL,
		SLOURL:       sloURL,
		IDPEntityID:  cfg.IDPEntityID,
		IDPSSOURL:    cfg.SSOURL,
		IDPSLOURL:    cfg.SLOURL,
		IDPCertPEM:   certPEM,
		NameIDFormat: cfg.NameIDFormat,
	}
	assertion, err := saml.ParseAndValidateResponse(samlCfg, samlResponse)
	if err != nil {
		logSecurityEvent(r, "saml_signature_invalid", "provider", provider, "tenant_id", tenantID, "error", err.Error())
		samlErrorPage(w, http.StatusBadRequest)
		return
	}

	email := assertion.Email
	identity, err := h.cp.IdentityByEmail(ctx, email)
	var fullName string
	switch {
	case errors.Is(err, tenancy.ErrIdentityNotFound):
		// JIT provisioning: create both the control-plane identity and (below)
		// the tenant-DB user row. The user gets cfg.DefaultRoleID if the admin
		// configured one (below, once the tenant-DB user row exists -- roles
		// are tenant-DB scoped), otherwise no role at all, same as before.
		fullName = bestEffortNameFromAssertion(assertion)
		identity, err = h.cp.CreateSSOIdentity(ctx, tenantID, email, fullName, provider, assertion.NameID)
		if err != nil {
			logSecurityEvent(r, "saml_identity_create_failed", "provider", provider, "tenant_id", tenantID, "error", err.Error())
			samlErrorPage(w, http.StatusInternalServerError)
			return
		}
	case err != nil:
		logSecurityEvent(r, "saml_acs_identity_lookup_failed", "provider", provider, "tenant_id", tenantID, "error", err.Error())
		samlErrorPage(w, http.StatusInternalServerError)
		return
	default:
		// Cross-tenant email collision guard: an identity by this email
		// exists, but not for the tenant this assertion was issued for.
		if identity.TenantID != tenantID {
			logSecurityEvent(r, "saml_identity_tenant_mismatch", "provider", provider, "identity_id", identity.ID,
				"assertion_tenant_id", tenantID, "identity_tenant_id", identity.TenantID)
			samlErrorPage(w, http.StatusForbidden)
			return
		}
		fullName = identity.FullName
		if identity.SSOProvider != "" && identity.SSOProvider != provider {
			logSecurityEvent(r, "saml_identity_provider_changed", "identity_id", identity.ID,
				"previous_provider", identity.SSOProvider, "new_provider", provider)
		}
		if err := h.cp.LinkSSOIdentity(ctx, identity.ID, provider, assertion.NameID, assertion.SessionIndex); err != nil {
			logSecurityEvent(r, "saml_identity_link_failed", "provider", provider, "identity_id", identity.ID, "error", err.Error())
			samlErrorPage(w, http.StatusInternalServerError)
			return
		}
	}

	pool, err := h.router.PoolFor(ctx, tenant)
	if err != nil {
		logSecurityEvent(r, "saml_acs_pool_failed", "provider", provider, "tenant_id", tenantID, "error", err.Error())
		samlErrorPage(w, http.StatusInternalServerError)
		return
	}
	user, err := userstore.GetUserByIdentityID(ctx, pool, identity.ID)
	switch {
	case errors.Is(err, userstore.ErrUserNotFound):
		user, err = userstore.CreateUser(ctx, pool, identity.ID, email, fullName, "active")
		if err != nil {
			logSecurityEvent(r, "saml_user_create_failed", "provider", provider, "identity_id", identity.ID, "tenant_id", tenantID, "error", err.Error())
			samlErrorPage(w, http.StatusInternalServerError)
			return
		}
		logSecurityEvent(r, "saml_jit_user_provisioned", "identity_id", identity.ID, "tenant_id", tenantID, "user_id", user.ID)
		// Grant the config's default role, if the admin set one. Non-fatal on
		// failure -- the user still gets a working login, just with no role
		// (the original behaviour) until an admin assigns one manually.
		if cfg.DefaultRoleID != "" {
			if err := authz.AssignRole(ctx, pool, user.ID, cfg.DefaultRoleID); err != nil {
				logSecurityEvent(r, "saml_default_role_assign_failed", "identity_id", identity.ID, "tenant_id", tenantID,
					"user_id", user.ID, "role_id", cfg.DefaultRoleID, "error", err.Error())
			} else {
				logSecurityEvent(r, "saml_default_role_assigned", "identity_id", identity.ID, "tenant_id", tenantID,
					"user_id", user.ID, "role_id", cfg.DefaultRoleID)
			}
		}
	case err != nil:
		logSecurityEvent(r, "saml_acs_user_lookup_failed", "provider", provider, "identity_id", identity.ID, "tenant_id", tenantID, "error", err.Error())
		samlErrorPage(w, http.StatusInternalServerError)
		return
	default:
		if user.Status == "suspended" || user.Status == "disabled" {
			logSecurityEvent(r, "saml_login_blocked_suspended_user", "identity_id", identity.ID, "tenant_id", tenantID, "user_id", user.ID, "status", user.Status)
			samlErrorPage(w, http.StatusForbidden)
			return
		}
	}

	// Mint a short-lived, single-use login code instead of putting the JWT
	// directly in the redirect URL -- avoids leaking it via browser history
	// or Referer headers. The frontend exchanges it via Exchange below.
	code, err := randomToken()
	if err != nil {
		logSecurityEvent(r, "saml_login_code_failed", "provider", provider, "identity_id", identity.ID, "tenant_id", tenantID, "error", err.Error())
		samlErrorPage(w, http.StatusInternalServerError)
		return
	}
	if err := h.cp.CreateSAMLLoginCode(ctx, code, identity.ID, tenantID, samlLoginCodeTTL); err != nil {
		logSecurityEvent(r, "saml_login_code_failed", "provider", provider, "identity_id", identity.ID, "tenant_id", tenantID, "error", err.Error())
		samlErrorPage(w, http.StatusInternalServerError)
		return
	}

	logSecurityEvent(r, "saml_login_completed", "identity_id", identity.ID, "tenant_id", tenantID, "provider", provider, "email", email)
	redirectURL := frontendBase() + "/auth/sso/callback?code=" + url.QueryEscape(code)
	if returnTo := r.FormValue("RelayState"); returnTo != "" && safeReturnTo(returnTo) {
		redirectURL += "&return_to=" + url.QueryEscape(returnTo)
	}
	http.Redirect(w, r, redirectURL, http.StatusFound)
}
