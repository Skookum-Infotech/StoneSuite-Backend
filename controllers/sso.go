package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/saml"
	"stonesuite-backend/secret"
	"stonesuite-backend/tenancy"
)

// SSOOps groups the tenant-scoped SSO-configuration handlers. Configs live in
// the control-plane database (tenant_sso_configs), keyed by the caller's tenant
// id — so every query is tenant-scoped by construction and a cross-tenant id
// reads as 404. The client secret is encrypted at rest via the cipher and is
// never returned to callers.
type SSOOps struct {
	cp     *tenancy.ControlPlane
	cipher *secret.Cipher
}

// NewSSOOps constructs the SSO handler group. cipher may be nil in development
// (no SECRET_ENCRYPTION_KEY); writes then fail closed with 503 rather than
// persisting a client secret in plaintext.
func NewSSOOps(cp *tenancy.ControlPlane, cipher *secret.Cipher) *SSOOps {
	return &SSOOps{cp: cp, cipher: cipher}
}

// ssoProviders is the whitelist of supported identity providers.
var ssoProviders = map[string]bool{"entra": true, "cognito": true, "okta": true}

// samlProviders restricts protocol="saml" configs to the providers this SAML
// implementation actually supports. Okta SAML is out of scope for this
// implementation (OIDC/Okta is unaffected and keeps working via ssoProviders).
var samlProviders = map[string]bool{"entra": true, "cognito": true}

// ssoProtocolOIDC and ssoProtocolSAML are the two protocol values
// tenant_sso_configs.protocol accepts.
const (
	ssoProtocolOIDC = "oidc"
	ssoProtocolSAML = "saml"
)

// ssoConfigRequest is the write payload. ClientSecret is optional on update
// (omit to keep the stored value) and required on create for protocol="oidc"
// only -- protocol="saml" configs never carry an OAuth client secret.
// MetadataURL is required when protocol="saml" and is (re-)fetched on every
// create/update.
type ssoConfigRequest struct {
	Provider     string `json:"provider"`
	Protocol     string `json:"protocol"` // "oidc" (default if empty) or "saml"
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Issuer       string `json:"issuer"`
	RedirectURI  string `json:"redirect_uri"`
	MetadataURL  string `json:"metadata_url"` // required when protocol="saml"
	Enabled      bool   `json:"enabled"`
}

// authorizeSSO resolves the caller, checks sso_config:action, and returns the
// control-plane tenant id to scope every query by. On failure it writes the
// response and returns ok=false.
func (h *SSOOps) authorizeSSO(w http.ResponseWriter, r *http.Request, action authz.Action) (tenantID, identityID string, ok bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return "", "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return "", "", false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return "", "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceSSOConfig, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceSSOConfig), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" SSO configurations.")
		return "", "", false
	}
	return tenant.ID, payload.ID, true
}

// ListConfigs GET /api/tenant/sso-configs
func (h *SSOOps) ListConfigs(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := h.authorizeSSO(w, r, authz.ActionRead)
	if !ok {
		return
	}
	configs, err := h.cp.ListSSOConfigs(r.Context(), tenantID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list SSO configurations.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "sso_configs": configs})
}

// GetConfig GET /api/tenant/sso-configs/{id}
func (h *SSOOps) GetConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := h.authorizeSSO(w, r, authz.ActionRead)
	if !ok {
		return
	}
	cfg, err := h.cp.GetSSOConfig(r.Context(), tenantID, r.PathValue("id"))
	if errors.Is(err, tenancy.ErrSSOConfigNotFound) {
		fail(w, http.StatusNotFound, "SSO configuration not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load SSO configuration.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "sso_config": cfg})
}

// CreateConfig POST /api/tenant/sso-configs
func (h *SSOOps) CreateConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := h.authorizeSSO(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var req ssoConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	in, msg := validateSSORequest(req, true)
	if msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}

	var encSecret, encCertPEM, certFingerprint string
	if in.Protocol == ssoProtocolSAML {
		meta, err := saml.FetchIdPMetadata(r.Context(), in.MetadataURL)
		if err != nil {
			fail(w, http.StatusBadGateway, "Could not fetch or parse identity provider metadata: "+err.Error())
			return
		}
		in.IDPEntityID = meta.EntityID
		in.SSOURL = meta.SSOURL
		in.SLOURL = meta.SLOURL
		certFingerprint = meta.Fingerprint
		enc, ok := h.encryptSecret(w, meta.CertificatePEM)
		if !ok {
			return
		}
		encCertPEM = enc
	} else {
		enc, ok := h.encryptSecret(w, req.ClientSecret)
		if !ok {
			return
		}
		encSecret = enc
	}

	cfg, err := h.cp.CreateSSOConfig(r.Context(), tenantID, in, encSecret, encCertPEM, certFingerprint)
	if isUniqueViolation(err) {
		fail(w, http.StatusConflict, "An SSO configuration for this provider already exists.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create SSO configuration.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "sso_config": cfg})
}

// UpdateConfig PUT /api/tenant/sso-configs/{id}
func (h *SSOOps) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := h.authorizeSSO(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var req ssoConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	in, msg := validateSSORequest(req, false)
	if msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	// Secret is optional on update: encrypt only when a new one is supplied.
	// (protocol="saml" requests never populate ClientSecret, so this stays a
	// no-op for them -- no special-casing needed.)
	var encSecret *string
	if strings.TrimSpace(req.ClientSecret) != "" {
		enc, ok := h.encryptSecret(w, req.ClientSecret)
		if !ok {
			return
		}
		encSecret = &enc
	}

	// metadata_url is required in the payload on every SAML update (see
	// validateSSORequest), so there is no "unchanged" case to special-case --
	// unlike the client secret, always re-fetch metadata when protocol=saml.
	var encCertPEM, certFingerprint *string
	var metadataFetchedAt *time.Time
	if in.Protocol == ssoProtocolSAML {
		meta, err := saml.FetchIdPMetadata(r.Context(), in.MetadataURL)
		if err != nil {
			fail(w, http.StatusBadGateway, "Could not fetch or parse identity provider metadata: "+err.Error())
			return
		}
		in.IDPEntityID = meta.EntityID
		in.SSOURL = meta.SSOURL
		in.SLOURL = meta.SLOURL
		enc, ok := h.encryptSecret(w, meta.CertificatePEM)
		if !ok {
			return
		}
		encCertPEM = &enc
		fingerprint := meta.Fingerprint
		certFingerprint = &fingerprint
		now := time.Now()
		metadataFetchedAt = &now
	}

	cfg, err := h.cp.UpdateSSOConfig(r.Context(), tenantID, r.PathValue("id"), in, encSecret, encCertPEM, certFingerprint, metadataFetchedAt)
	if errors.Is(err, tenancy.ErrSSOConfigNotFound) {
		fail(w, http.StatusNotFound, "SSO configuration not found.")
		return
	}
	if isUniqueViolation(err) {
		fail(w, http.StatusConflict, "An SSO configuration for this provider already exists.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update SSO configuration.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "sso_config": cfg})
}

// DeleteConfig DELETE /api/tenant/sso-configs/{id}
func (h *SSOOps) DeleteConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := h.authorizeSSO(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	err := h.cp.DeleteSSOConfig(r.Context(), tenantID, r.PathValue("id"))
	if errors.Is(err, tenancy.ErrSSOConfigNotFound) {
		fail(w, http.StatusNotFound, "SSO configuration not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to delete SSO configuration.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// RefreshMetadata re-fetches the IdP metadata document for an existing
// protocol="saml" config and updates the stored SSO/SLO URLs and certificate.
// Safe to call repeatedly. Returns 400 if the target config is not
// protocol="saml".
//
// Route: POST /api/tenant/sso-configs/{id}/refresh-metadata
//
// Known limitation: this is a manual, admin-triggered refresh only. An
// hourly background auto-refresh (to rotate IdP certificates before they
// expire) was considered and descoped from this pass to avoid introducing a
// new goroutine-lifecycle subsystem; a future scheduled task can call this
// same endpoint.
func (h *SSOOps) RefreshMetadata(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := h.authorizeSSO(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	cfg, err := h.cp.GetSSOConfig(r.Context(), tenantID, r.PathValue("id"))
	if errors.Is(err, tenancy.ErrSSOConfigNotFound) {
		fail(w, http.StatusNotFound, "SSO configuration not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load SSO configuration.")
		return
	}
	if cfg.Protocol != ssoProtocolSAML {
		fail(w, http.StatusBadRequest, "Metadata refresh only applies to protocol=saml configurations.")
		return
	}

	meta, err := saml.FetchIdPMetadata(r.Context(), cfg.MetadataURL)
	if err != nil {
		fail(w, http.StatusBadGateway, "Could not fetch or parse identity provider metadata: "+err.Error())
		return
	}
	encCert, ok := h.encryptSecret(w, meta.CertificatePEM)
	if !ok {
		return
	}

	in := tenancy.SSOConfigInput{
		Provider:     cfg.Provider,
		Protocol:     cfg.Protocol,
		ClientID:     cfg.ClientID,
		Issuer:       cfg.Issuer,
		RedirectURI:  cfg.RedirectURI,
		MetadataURL:  cfg.MetadataURL,
		IDPEntityID:  meta.EntityID,
		SSOURL:       meta.SSOURL,
		SLOURL:       meta.SLOURL,
		NameIDFormat: cfg.NameIDFormat,
		Enabled:      cfg.Enabled,
	}
	certFingerprint := meta.Fingerprint
	now := time.Now()
	// encSecret=nil leaves the client secret untouched -- irrelevant for SAML
	// configs, which never have one.
	updated, err := h.cp.UpdateSSOConfig(r.Context(), tenantID, cfg.ID, in, nil, &encCert, &certFingerprint, &now)
	if errors.Is(err, tenancy.ErrSSOConfigNotFound) {
		fail(w, http.StatusNotFound, "SSO configuration not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to refresh SSO configuration metadata.")
		return
	}

	logSecurityEvent(r, "saml_metadata_refreshed", "provider", cfg.Provider, "sso_config_id", cfg.ID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "sso_config": updated})
}

// encryptSecret encrypts a secret (an OAuth client secret or a SAML IdP
// certificate PEM), failing closed (503) when no cipher is configured so a
// secret is never persisted in plaintext. On failure it writes the response
// and returns ok=false.
func (h *SSOOps) encryptSecret(w http.ResponseWriter, plaintext string) (string, bool) {
	if h.cipher == nil {
		fail(w, http.StatusServiceUnavailable, "SSO configuration requires secret encryption to be enabled.")
		return "", false
	}
	enc, err := h.cipher.Encrypt(plaintext)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to secure the client secret.")
		return "", false
	}
	return enc, true
}

// validateSSORequest validates and normalizes the payload. requireSecret is
// true on create for protocol="oidc" only (SAML configs never have an OAuth
// client secret). It returns the store input and an empty message on
// success, or a non-empty error message to send as 400.
func validateSSORequest(req ssoConfigRequest, requireSecret bool) (tenancy.SSOConfigInput, string) {
	protocol := strings.ToLower(strings.TrimSpace(req.Protocol))
	if protocol == "" {
		protocol = ssoProtocolOIDC // backward-compatible default: every existing caller omits this field
	}
	if protocol != ssoProtocolOIDC && protocol != ssoProtocolSAML {
		return tenancy.SSOConfigInput{}, "protocol must be one of: oidc, saml."
	}

	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if protocol == ssoProtocolSAML {
		if !samlProviders[provider] {
			return tenancy.SSOConfigInput{}, "For protocol=saml, provider must be one of: entra, cognito."
		}
	} else {
		if !ssoProviders[provider] {
			return tenancy.SSOConfigInput{}, "Provider must be one of: entra, cognito, okta."
		}
	}

	in := tenancy.SSOConfigInput{Provider: provider, Protocol: protocol, Enabled: req.Enabled}

	if protocol == ssoProtocolSAML {
		metadataURL := strings.TrimSpace(req.MetadataURL)
		if metadataURL == "" {
			return tenancy.SSOConfigInput{}, "metadata_url is required for protocol=saml."
		}
		if !strings.HasPrefix(metadataURL, "https://") {
			return tenancy.SSOConfigInput{}, "metadata_url must be an https:// URL."
		}
		in.MetadataURL = metadataURL
		// NameIDFormat: leave blank so the DB default
		// (urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress) applies --
		// do not hardcode the default string here, that's the schema's job
		// (see tenancy/sso_config.go's default-handling).
		return in, ""
	}

	// protocol == "oidc": unchanged existing behavior, byte-for-byte.
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		return tenancy.SSOConfigInput{}, "client_id is required."
	}
	if requireSecret && strings.TrimSpace(req.ClientSecret) == "" {
		return tenancy.SSOConfigInput{}, "client_secret is required."
	}
	issuer := strings.TrimSpace(req.Issuer)
	if issuer != "" && !isHTTPURL(issuer) {
		return tenancy.SSOConfigInput{}, "issuer must be a valid http(s) URL."
	}
	redirect := strings.TrimSpace(req.RedirectURI)
	if redirect != "" && !isHTTPURL(redirect) {
		return tenancy.SSOConfigInput{}, "redirect_uri must be a valid http(s) URL."
	}
	in.ClientID = clientID
	in.Issuer = issuer
	in.RedirectURI = redirect
	return in, ""
}

// isHTTPURL reports whether s parses as an absolute http or https URL.
func isHTTPURL(s string) bool {
	u, err := url.ParseRequestURI(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
