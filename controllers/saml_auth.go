package controllers

import (
	"errors"
	"net/http"
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
// Path: GET /api/auth/saml/{provider}/initiate?tenant_slug=...|tenant_id=...
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
	if (slug != "") == (tenantID != "") {
		fail(w, http.StatusBadRequest, "Provide exactly one of tenant_slug or tenant_id.")
		return
	}

	var tenant *tenancy.Tenant
	var err error
	if slug != "" {
		tenant, err = h.cp.TenantBySlug(ctx, slug)
	} else {
		tenant, err = h.cp.TenantByID(ctx, tenantID)
	}
	if errors.Is(err, tenancy.ErrTenantNotFound) {
		fail(w, http.StatusNotFound, "Workspace not found.")
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
		fail(w, http.StatusNotFound, "SAML sign-in is not configured or enabled for this workspace.")
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
