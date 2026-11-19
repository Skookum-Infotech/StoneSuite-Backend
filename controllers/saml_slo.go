package controllers

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/middleware"
	"stonesuite-backend/saml"
	"stonesuite-backend/tenancy"
)

const (
	// maxSLOBodyBytes caps a POST-binding logout request body.
	maxSLOBodyBytes = 64 << 10
	// maxSLOQueryBytes caps a redirect-binding logout request's query string.
	maxSLOQueryBytes = 32 << 10

	// sloRejectedMessage is the one answer to every refused logout request, so
	// the reason (and so whether a name or issuer exists) is not an oracle.
	sloRejectedMessage = "Invalid logout request."

	paramSAMLRequest  = "SAMLRequest"
	paramSAMLResponse = "SAMLResponse"
	paramRelayState   = "RelayState"
)

// endLocalSession ends the caller's own server-side session: the one behind
// their refresh-token cookie and the one the access token was issued for.
// Clearing cookies alone leaves both tokens working until they expire, which is
// not a logout. It reports false, having already written the error, only when
// the refresh token could not be deleted.
func (h *SAMLAuthOps) endLocalSession(w http.ResponseWriter, r *http.Request, payload middleware.UserContextPayload) bool {
	ctx := r.Context()
	if payload.SessionID != "" {
		if err := h.cp.EndSession(ctx, payload.SessionID); err != nil {
			slog.Warn("saml logout: end session", "error", err)
		} else {
			h.sessions.Forget(payload.SessionID)
		}
	}
	if cookie, err := r.Cookie("refresh_token"); err == nil && cookie.Value != "" {
		hash := tenancy.HashRefreshToken(cookie.Value)
		endSessionForRefreshToken(r, h.cp, h.sessions, hash)
		if err := h.cp.DeleteRefreshToken(ctx, hash); err != nil {
			slog.Error("saml logout: delete refresh token", "error", err)
			clearAuthCookies(w)
			fail(w, http.StatusInternalServerError, "Failed to end session. Please try again.")
			return false
		}
	}
	return true
}

// sloRequestParts returns the encoded LogoutRequest and RelayState of an
// inbound request and which binding carried them. ok is false when there is no
// LogoutRequest at all (a LogoutResponse, or a bare visit).
func sloRequestParts(w http.ResponseWriter, r *http.Request) (encoded, relayState string, redirectBinding, ok bool) {
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, maxSLOBodyBytes)
		if err := r.ParseForm(); err != nil {
			return "", "", false, false
		}
		encoded = r.PostForm.Get(paramSAMLRequest)
		return encoded, r.PostForm.Get(paramRelayState), false, encoded != ""
	}
	q := r.URL.Query()
	return q.Get(paramSAMLRequest), q.Get(paramRelayState), true, q.Has(paramSAMLRequest)
}

// sloReason is a stable, log-safe label for why a logout request was refused.
func sloReason(err error) string {
	switch {
	case errors.Is(err, saml.ErrLogoutNotSigned):
		return "not_signed"
	case errors.Is(err, saml.ErrLogoutBadSignature):
		return "bad_signature"
	case errors.Is(err, saml.ErrLogoutWrongIssuer):
		return "wrong_issuer"
	case errors.Is(err, saml.ErrLogoutWrongTarget):
		return "wrong_target"
	case errors.Is(err, saml.ErrLogoutExpired):
		return "expired"
	case errors.Is(err, saml.ErrLogoutNoPrincipal):
		return "no_principal"
	case errors.Is(err, saml.ErrLogoutMalformed):
		return "malformed"
	default:
		return "unverifiable"
	}
}

// idpLogoutRequest handles a LogoutRequest the identity provider sends because a
// user's session ended there: verify it, end that user's sessions here, and
// answer the IdP with a LogoutResponse. It is reached through the SP's single
// logout endpoint (the Location in our metadata), with either binding.
//
// Order is the security design. The issuer is read unverified only to pick whose
// certificate to check; nothing is acted on until the signature verifies against
// a tenant's own IdP certificate; the principal is then looked up inside THAT
// tenant only, so a request one tenant's IdP can sign can never end another
// tenant's session; and the request id is claimed once, so a replay does nothing.
func (h *SAMLAuthOps) idpLogoutRequest(w http.ResponseWriter, r *http.Request, provider, encoded, relayState string, redirectBinding bool) {
	ctx := r.Context()
	reject := func(reason string, kv ...any) {
		logSecurityEvent(r, "saml_slo_rejected", append([]any{"provider", provider, "reason", reason}, kv...)...)
		fail(w, http.StatusBadRequest, sloRejectedMessage)
	}
	if h.cipher == nil {
		slog.Error("saml slo: no cipher configured")
		fail(w, http.StatusServiceUnavailable, "Single logout is not available.")
		return
	}
	if redirectBinding && len(r.URL.RawQuery) > maxSLOQueryBytes {
		reject("malformed")
		return
	}

	issuer, err := saml.UnverifiedLogoutIssuer(encoded, redirectBinding)
	if err != nil || issuer == "" {
		reject(sloReason(err))
		return
	}
	configs, err := h.cp.SSOConfigsByIssuer(ctx, provider, issuer)
	if err != nil {
		slog.Error("saml slo: load configs", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to process logout request.")
		return
	}
	if len(configs) == 0 {
		reject("unknown_issuer")
		return
	}

	_, acsURL, sloURL := spConfig(provider)
	spEntityID, _, _ := spConfig(provider)
	now := time.Now()
	var (
		req     *saml.LogoutRequest
		matched *tenancy.SSOConfigAuth
		samlCfg saml.Config
		lastErr error
	)
	for i := range configs {
		cfg := configs[i]
		certPEM, derr := h.cipher.Decrypt(cfg.CertificatePEMEnc)
		if derr != nil {
			lastErr = derr
			continue
		}
		c := saml.Config{
			SPEntityID: spEntityID, ACSURL: acsURL, SLOURL: sloURL,
			IDPEntityID: cfg.IDPEntityID, IDPSSOURL: cfg.SSOURL, IDPSLOURL: cfg.SLOURL,
			IDPCertPEM: certPEM, NameIDFormat: cfg.NameIDFormat,
		}
		var got *saml.LogoutRequest
		if redirectBinding {
			got, err = saml.ParseLogoutRequestRedirect(c, r.URL.RawQuery, now)
		} else {
			got, err = saml.ParseLogoutRequestPOST(c, encoded, now)
		}
		if err != nil {
			lastErr = err
			continue
		}
		req, matched, samlCfg = got, &cfg, c
		break
	}
	if req == nil {
		reject(sloReason(lastErr))
		return
	}

	// Verified. Claim the id before acting so a replay does nothing.
	first, err := h.cp.ClaimSAMLLogoutRequest(ctx, req.Issuer, req.ID, req.ExpiresAt)
	if err != nil {
		slog.Error("saml slo: claim request", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to process logout request.")
		return
	}
	if !first {
		reject("replayed", "tenant_id", matched.TenantID)
		return
	}

	identity, err := h.cp.IdentityBySSOSubject(ctx, matched.TenantID, provider, req.NameID)
	switch {
	case errors.Is(err, tenancy.ErrSSOIdentityNotFound):
		// Logout is idempotent: answering Success tells the IdP nothing about
		// which names exist here.
		logSecurityEvent(r, "saml_slo_unknown_principal", "provider", provider, "tenant_id", matched.TenantID)
	case err != nil:
		slog.Error("saml slo: load identity", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to process logout request.")
		return
	default:
		if err := h.endIdentitySessions(r, identity.ID); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to process logout request.")
			return
		}
		logSecurityEvent(r, "saml_slo_session_ended", "provider", provider,
			"tenant_id", matched.TenantID, "identity_id", identity.ID)
	}

	// This browser belongs to the user the IdP just signed out.
	clearAuthCookies(w)
	if redirectURL, err := saml.BuildLogoutResponseURL(samlCfg, req.ID, relayState); err == nil {
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}
	http.Redirect(w, r, frontendBase()+"/auth/login?logged_out=true", http.StatusFound)
}

// endIdentitySessions ends every session and refresh token an identity has.
func (h *SAMLAuthOps) endIdentitySessions(r *http.Request, identityID string) error {
	if err := h.cp.RevokeAllRefreshTokens(r.Context(), identityID); err != nil {
		slog.Error("saml slo: revoke sessions", "identity_id", identityID, "error", err)
		return err
	}
	return nil
}
