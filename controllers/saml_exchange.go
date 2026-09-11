package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/config"
)

// samlExchangeRequest is the POST /api/auth/saml/exchange payload: the
// short-lived, single-use login code minted by ACS on success.
type samlExchangeRequest struct {
	Code string `json:"code"`
}

// Exchange trades a short-lived SAML login code (minted by ACS) for the real
// JWT, mirroring TenantLogin's response shape and cookie-setting exactly so
// a frontend integration can reuse its existing setAuth(user, token,
// expiresAt) call pattern. Path: POST /api/auth/saml/exchange
func (h *SAMLAuthOps) Exchange(w http.ResponseWriter, r *http.Request) {
	var req samlExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if req.Code == "" {
		fail(w, http.StatusBadRequest, "code is required.")
		return
	}

	ctx := r.Context()
	identityID, _, err := h.cp.ConsumeSAMLLoginCode(ctx, req.Code)
	if err != nil {
		logSecurityEvent(r, "saml_exchange_invalid_code")
		fail(w, http.StatusBadRequest, "Invalid or expired sign-in code.")
		return
	}

	identity, err := h.cp.IdentityByID(ctx, identityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load account.")
		return
	}

	d, err := time.ParseDuration(config.AppConfig.JWTExpiresIn)
	if err != nil {
		d = time.Hour
	}
	// Best-effort pool resolution for accessible_resources, same shape
	// tenantDisplayName below uses — tolerant of any failure since this is
	// enrichment, not the identity/session trust check (already done by ACS
	// before this login code was minted).
	var pool *pgxpool.Pool
	if identity.TenantID != "" {
		if tenant, tErr := h.cp.TenantByID(ctx, identity.TenantID); tErr == nil && tenant.Servable() {
			if p, pErr := h.router.PoolFor(ctx, tenant); pErr == nil {
				pool = p
			}
		}
	}
	accessibleResources := resolveAccessibleResources(ctx, pool, identity.ID, "")
	token, err := generateTenantJWT(identity.ID, identity.Email, identity.TenantID, "", accessibleResources, d)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to sign token.")
		return
	}

	// Non-fatal, exactly like TenantLogin: default false on lookup failure.
	isPlatformAdmin, err := h.cp.IsPlatformAdmin(ctx, identity.ID)
	if err != nil {
		isPlatformAdmin = false
	}

	// Non-fatal, exactly like TenantLogin: the session still works without a
	// refresh token, the caller just re-authenticates when the access token
	// expires.
	refreshRaw, refreshExpiry, err := issueRefreshToken(ctx, h.cp, identity.ID)
	if err != nil {
		slog.Warn("saml exchange: failed to issue refresh token",
			slog.String("identity_id", identity.ID), slog.String("error", err.Error()))
		refreshRaw = ""
	}

	accessExpiry := time.Now().Add(d)
	if err := setAuthCookies(w, token, d, refreshRaw, refreshExpiry); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to establish session.")
		return
	}

	logSecurityEvent(r, "saml_exchange_completed", "identity_id", identity.ID, "tenant_id", identity.TenantID)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"token":     token,
		"expiresAt": accessExpiry.UnixMilli(),
		"user": map[string]any{
			"id": identity.ID, "email": identity.Email,
			"fullName": tenantDisplayName(ctx, h.cp, h.router, identity), "tenantId": identity.TenantID,
			"isPlatformAdmin": isPlatformAdmin,
		},
	})
}
