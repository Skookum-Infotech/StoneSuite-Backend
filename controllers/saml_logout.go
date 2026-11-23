package controllers

import (
	"net/http"

	"stonesuite-backend/middleware"
	"stonesuite-backend/saml"
)

// Logout ends the caller's own session (server side, not just its cookies) and,
// if the identity was established via SAML and the IdP advertises a Single
// Logout endpoint, returns a logout_url the frontend should navigate the
// browser to next. This is the SP-initiated half of single logout; the
// IdP-initiated half, where the IdP tells us a session ended there, arrives at
// LogoutResponse's endpoint (see saml_slo.go).
// Path: POST /api/auth/saml/{provider}/logout (auth required)
func (h *SAMLAuthOps) Logout(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if !isValidSAMLProvider(provider) {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	ctx := r.Context()

	payload, err := middleware.GetUserFromContext(ctx)
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}

	identity, err := h.cp.IdentityByID(ctx, payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load account.")
		return
	}
	if identity.SSOProvider != provider || identity.SSOSubject == "" {
		fail(w, http.StatusBadRequest, "This session was not established via SAML sign-in.")
		return
	}

	tenant, err := h.cp.TenantByID(ctx, payload.TenantID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workspace.")
		return
	}

	cfg, cfgErr := h.cp.GetSSOConfigForAuth(ctx, tenant.ID, provider)

	// End our own session regardless of SLO outcome below -- local logout
	// must never depend on the IdP being reachable, configured, or even
	// SAML-capable of SLO at all. Clearing the cookies alone would leave the
	// access and refresh tokens working until they expire.
	if !h.endLocalSession(w, r, payload) {
		return
	}
	clearAuthCookies(w)

	if cfgErr != nil || cfg.SLOURL == "" || h.cipher == nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "slo_available": false})
		return
	}
	certPEM, err := h.cipher.Decrypt(cfg.CertificatePEMEnc)
	if err != nil {
		// Best-effort: local logout already happened above, don't fail the
		// whole request over SLO.
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "slo_available": false})
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
	logoutURL, err := saml.BuildLogoutRequestURL(samlCfg, identity.SSOSubject, identity.SSOSessionIndex, "")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "slo_available": false})
		return
	}

	logSecurityEvent(r, "saml_logout_initiated", "identity_id", identity.ID, "provider", provider)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "slo_available": true, "logout_url": logoutURL})
}

// LogoutResponse is the SP's single logout endpoint -- the SingleLogoutService
// Location advertised in SP metadata and the Destination of our LogoutRequest
// (see spConfig). SAML uses one endpoint for both directions, so two things
// arrive here:
//
//   - A LogoutRequest from the IdP, because a session ended there: verified and
//     acted on by idpLogoutRequest (saml_slo.go).
//   - The IdP's LogoutResponse to the request Logout sent. That logout already
//     ended the local session before redirecting to the IdP, so there is no
//     security decision left; the return trip just lands on the login page
//     rather than a 404, whatever the IdP sends back.
//
// Path: GET and POST /api/auth/saml/{provider}/logout-response
func (h *SAMLAuthOps) LogoutResponse(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if !isValidSAMLProvider(provider) {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	if encoded, relayState, redirectBinding, ok := sloRequestParts(w, r); ok {
		h.idpLogoutRequest(w, r, provider, encoded, relayState, redirectBinding)
		return
	}
	http.Redirect(w, r, frontendBase()+"/auth/login?logged_out=true", http.StatusFound)
}
