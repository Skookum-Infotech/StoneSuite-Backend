package controllers

import (
	"net/http"

	"stonesuite-backend/middleware"
	"stonesuite-backend/saml"
)

// Logout clears the caller's local session and, if the identity was
// established via SAML and the IdP advertises a Single Logout endpoint,
// returns a logout_url the frontend should navigate the browser to next.
// SP-initiated only: IdP-initiated logout, where the IdP calls back to tell
// the SP a session ended elsewhere, is not implemented -- see the step
// report. Path: POST /api/auth/saml/{provider}/logout (auth required)
func (h *SAMLAuthOps) Logout(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if !samlProviders[provider] {
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

	// Clear our own session regardless of SLO outcome below -- local logout
	// must never depend on the IdP being reachable, configured, or even
	// SAML-capable of SLO at all.
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

// LogoutResponse is the SP-side endpoint the IdP redirects the browser back
// to after processing our LogoutRequest -- it is advertised in SP metadata
// and as the Destination of that request (see spConfig). Logout above
// already cleared the local session before ever redirecting to the IdP, so
// there is no security decision left to make on the way back; this handler
// exists only so that return trip lands somewhere other than a 404, whatever
// the IdP sends (or doesn't send) back. Path: GET /api/auth/saml/{provider}/logout-response
func (h *SAMLAuthOps) LogoutResponse(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if !samlProviders[provider] {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	http.Redirect(w, r, frontendBase()+"/auth/login?logged_out=true", http.StatusFound)
}
