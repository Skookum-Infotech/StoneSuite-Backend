package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"stonesuite-backend/tenancy"
)

type identifyRequest struct {
	Email string `json:"email"`
}

// Identify tells the login page how to continue past its email step: "sso"
// (redirect to that provider's SAML flow) or "password" (show the password
// field). This is deliberately a function of the email's *domain* only,
// never of whether a specific account exists -- TenantLogin is the only
// place that checks a real identity, and only after a password is
// submitted. Every non-SSO outcome (unregistered domain, disabled config,
// unservable tenant, malformed email) collapses to the same "password"
// response, so this endpoint can never be used to enumerate which emails
// have StoneSuite accounts. Public, no auth, no tenant resolved yet -- same
// trust level as /api/auth/saml/discover, which this supersedes as the
// login page's routing decision.
// Path: POST /api/auth/identify
func (h *TenantOps) Identify(w http.ResponseWriter, r *http.Request) {
	var req identifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	ctx := r.Context()
	domain := tenancy.NormalizeEmailDomain(req.Email)
	if domain == "" {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "method": "password"})
		return
	}

	result, err := h.CP.DiscoverSSOByEmailDomain(ctx, domain)
	if errors.Is(err, tenancy.ErrSSOConfigNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "method": "password"})
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to resolve sign-in method.")
		return
	}

	tenant, err := h.CP.TenantByID(ctx, result.TenantID)
	if err != nil {
		logSecurityEvent(r, "identify_tenant_lookup_failed", "tenant_id", result.TenantID, "provider", result.Provider, "error", err.Error())
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "method": "password"})
		return
	}
	if !tenant.Servable() {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "method": "password"})
		return
	}

	logSecurityEvent(r, "identify_resolved_sso", "tenant_id", result.TenantID, "provider", result.Provider, "domain", domain)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "method": "sso",
		"provider": result.Provider, "tenant_id": result.TenantID,
	})
}
