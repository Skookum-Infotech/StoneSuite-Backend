package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"stonesuite-backend/config"
	"stonesuite-backend/saml"
	"stonesuite-backend/secret"
	"stonesuite-backend/tenancy"
)

// SAMLAuthOps groups the SAML authentication-flow handlers: metadata, login
// initiation, the assertion consumer service (ACS, saml_acs.go), the
// login-code exchange (saml_exchange.go), and SP-initiated logout
// (saml_logout.go). Distinct from SSOOps (controllers/sso.go), which only
// manages SAML/OIDC *configuration* CRUD.
type SAMLAuthOps struct {
	cp     *tenancy.ControlPlane
	router *tenancy.Router
	cipher *secret.Cipher
}

// NewSAMLAuthOps constructs the SAML auth handler group.
func NewSAMLAuthOps(cp *tenancy.ControlPlane, router *tenancy.Router, cipher *secret.Cipher) *SAMLAuthOps {
	return &SAMLAuthOps{cp: cp, router: router, cipher: cipher}
}

// samlSignInUnavailableMsg is the single canonical 404 body Initiate returns
// for every tenant/identity-resolution failure and every missing-SAML-config
// outcome, regardless of which of tenant_slug/tenant_id/email was supplied.
// An email is user PII (unlike a tenant_slug), so letting "identity not
// found" read any differently -- in status, message text, or timing-sensitive
// short-circuiting -- than "tenant not found" or "no SAML config for this
// tenant" would open an account-enumeration oracle this endpoint must not
// have; using one shared constant for all of them closes that by construction.
const samlSignInUnavailableMsg = "SAML sign-in is not configured or enabled for this workspace."

// spConfig builds this SP's per-provider entity id, ACS URL, and SP-side SLO
// URL, rooted at the backend's own public base URL (config.AppConfig.APIBaseURL)
// -- never the frontend's, which today only hosts non-functional placeholder
// setup-guide text pointing at the frontend's own domain.
func spConfig(provider string) (spEntityID, acsURL, sloURL string) {
	spEntityID = strings.TrimRight(config.AppConfig.SAMLSPEntityID, "/") + "/" + provider + "/metadata"
	acsURL = strings.TrimRight(config.AppConfig.APIBaseURL, "/") + "/api/auth/saml/" + provider + "/acs"
	sloURL = strings.TrimRight(config.AppConfig.APIBaseURL, "/") + "/api/auth/saml/" + provider + "/logout-response"
	return spEntityID, acsURL, sloURL
}

// safeReturnTo reports whether s is safe to carry as RelayState and later
// echo back as a post-login redirect target: a same-origin-relative path
// only. AuthnRequests are never signed (see saml.serviceProvider's doc
// comment), so RelayState has no integrity protection in transit -- an
// attacker can put anything in the initial ?return_to= query param on a
// phishing link. Without this check, a victim who completes a genuine SAML
// login would be bounced to an attacker-chosen URL immediately afterward
// (open redirect). Rejects absolute URLs (scheme or "//" protocol-relative)
// and backslashes some browsers normalize into forward slashes.
func safeReturnTo(s string) bool {
	if s == "" {
		return true
	}
	return strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.ContainsAny(s, "\\")
}

// Metadata serves this SP's SAML metadata document for provider. Public, no
// auth, no DB call. Path: GET /api/auth/saml/{provider}/metadata
func (h *SAMLAuthOps) Metadata(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if !samlProviders[provider] {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	spEntityID, acsURL, sloURL := spConfig(provider)
	xmlBytes, err := saml.SPMetadataXML(spEntityID, acsURL, sloURL)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate metadata.")
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(xmlBytes)
}

// Initiate begins a SAML login: resolves the tenant, builds an unsigned
// AuthnRequest (see saml.serviceProvider's doc comment for why AuthnRequests
// are never signed), records single-use request state keyed by the
// generated request id, and redirects the browser to the IdP's SSO endpoint.
// Accepts exactly one of tenant_slug, tenant_id, or email to resolve the
// tenant: email resolves via the caller's control-plane identity
// (IdentityByEmail) to its owning tenant, then joins the exact same
// downstream flow tenant_slug/tenant_id already use. Every unresolvable
// input and every missing-SAML-config outcome, for all three parameters,
// returns the identical samlSignInUnavailableMsg 404 -- see its doc comment.
// Path: GET /api/auth/saml/{provider}/initiate?tenant_slug=...|tenant_id=...|email=...
func (h *SAMLAuthOps) Initiate(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if !samlProviders[provider] {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	slug := strings.TrimSpace(q.Get("tenant_slug"))
	tenantID := strings.TrimSpace(q.Get("tenant_id"))
	email := strings.TrimSpace(q.Get("email"))
	provided := 0
	for _, v := range []string{slug, tenantID, email} {
		if v != "" {
			provided++
		}
	}
	if provided != 1 {
		fail(w, http.StatusBadRequest, "Provide exactly one of tenant_slug, tenant_id, or email.")
		return
	}

	var tenant *tenancy.Tenant
	var err error
	switch {
	case slug != "":
		tenant, err = h.cp.TenantBySlug(ctx, slug)
	case tenantID != "":
		tenant, err = h.cp.TenantByID(ctx, tenantID)
	default:
		var identity *tenancy.Identity
		identity, err = h.cp.IdentityByEmail(ctx, email)
		if errors.Is(err, tenancy.ErrIdentityNotFound) {
			fail(w, http.StatusNotFound, samlSignInUnavailableMsg)
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to resolve workspace.")
			return
		}
		tenant, err = h.cp.TenantByID(ctx, identity.TenantID)
	}
	if errors.Is(err, tenancy.ErrTenantNotFound) {
		fail(w, http.StatusNotFound, samlSignInUnavailableMsg)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to resolve workspace.")
		return
	}
	if !tenant.Servable() {
		fail(w, http.StatusForbidden, "This workspace is not available for sign-in.")
		return
	}

	cfg, err := h.cp.GetSSOConfigForAuth(ctx, tenant.ID, provider)
	if errors.Is(err, tenancy.ErrSSOConfigNotFound) {
		fail(w, http.StatusNotFound, samlSignInUnavailableMsg)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load SAML configuration.")
		return
	}

	// saml.Config's builder (serviceProvider) always constructs an IdP
	// certificate store, even for an AuthnRequest, which never needs one --
	// only response validation does. certificateStore errors on an empty
	// PEM, so an empty IDPCertPEM here would fail BuildAuthnRequestURL
	// outright. Decrypting the tenant's already-on-file certificate (the
	// same operation ACS performs) is the deviation from this handler's
	// original "skip decryption, it's not needed" design; see the step
	// report for the full reasoning.
	if h.cipher == nil {
		fail(w, http.StatusInternalServerError, "SAML sign-in is not available.")
		return
	}
	certPEM, err := h.cipher.Decrypt(cfg.CertificatePEMEnc)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load SAML configuration.")
		return
	}

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

	returnTo := q.Get("return_to")
	if !safeReturnTo(returnTo) {
		fail(w, http.StatusBadRequest, "return_to must be a relative path.")
		return
	}

	redirectURL, requestID, err := saml.BuildAuthnRequestURL(samlCfg, returnTo)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to build sign-in request.")
		return
	}

	if err := h.cp.CreateSAMLRequestState(ctx, requestID, tenant.ID, provider, config.AppConfig.SAMLRequestStateTTL); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to start sign-in.")
		return
	}

	logSecurityEvent(r, "saml_login_initiated", "tenant_id", tenant.ID, "provider", provider)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// samlLookupRequest is the POST /api/auth/saml/lookup payload: the email the
// frontend wants to know whether the owning workspace uses SAML SSO for.
type samlLookupRequest struct {
	Email string `json:"email"`
}

// Lookup reports whether the workspace owning email has SAML SSO enabled, so
// the frontend can offer an email-first "Continue with <provider>" button
// instead of a password field (the Slack/Notion/Google Workspace pattern).
// Only ever resolves against identities that already exist in the control
// plane -- brand-new emails are out of scope. Always responds 200: the
// response body (sso_available/providers), never the status code, carries
// the "email known vs unknown" / "SSO configured vs not" distinction. That
// distinction is this endpoint's entire, deliberate purpose (same trade-off
// those products make); varying the status code on top of it would
// additionally leak a timing/status-code oracle for raw account existence,
// which is not part of that trade-off. Never includes tenant_id, identity
// id, or any other identifier in the response.
// Path: POST /api/auth/saml/lookup
func (h *SAMLAuthOps) Lookup(w http.ResponseWriter, r *http.Request) {
	var req samlLookupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" {
		fail(w, http.StatusBadRequest, "email is required.")
		return
	}

	ctx := r.Context()
	identity, err := h.cp.IdentityByEmail(ctx, req.Email)
	if errors.Is(err, tenancy.ErrIdentityNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":       true,
			"sso_available": false,
			"providers":     []string{},
		})
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to process request.")
		return
	}

	configs, err := h.cp.ListSSOConfigs(ctx, identity.TenantID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to process request.")
		return
	}
	providers := make([]string, 0, len(configs))
	for _, cfg := range configs {
		if cfg.Protocol == ssoProtocolSAML && cfg.Enabled {
			providers = append(providers, cfg.Provider)
		}
	}
	sort.Strings(providers)

	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"sso_available": len(providers) > 0,
		"providers":     providers,
	})
}
