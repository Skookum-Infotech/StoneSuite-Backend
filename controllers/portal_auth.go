package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// PortalAuthOps groups the customer-portal authentication handlers.
//
// The portal is a separate credential surface from staff auth, sharing only the
// identities table underneath. Every handler here asserts the caller holds an
// active portal link before acting — the password-setup and reset token columns
// on identities are shared with staff, so without that assertion a staff setup
// token could be consumed through a portal endpoint, or the reverse.
type PortalAuthOps struct {
	CP     *tenancy.ControlPlane
	Router *tenancy.Router
}

// NewPortalAuthOps constructs the handler group.
func NewPortalAuthOps(cp *tenancy.ControlPlane, router *tenancy.Router) *PortalAuthOps {
	return &PortalAuthOps{CP: cp, Router: router}
}

// portalMinPasswordLen mirrors the staff minimum so the two surfaces do not
// diverge into one being weaker than the other.
const portalMinPasswordLen = 8

// workspaceView is one entry in the portal workspace switcher.
type workspaceView struct {
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Active   bool   `json:"active"`
}

// portalTokenDuration returns the portal access-token lifetime.
func portalTokenDuration() time.Duration {
	d, err := time.ParseDuration(config.AppConfig.JWTExpiresIn)
	if err != nil {
		return time.Hour
	}
	return d
}

// servableWorkspaces filters links down to workspaces that can actually serve a
// request, and marks which one is active. A suspended or still-provisioning
// tenant is omitted rather than offered and then rejected by the resolver.
//
// Package-level rather than a PortalAuthOps method so TenantOps.tryPortalLogin
// (the merged-login fallback in tenant.go) can call it too without needing a
// PortalAuthOps instance — both mint the same workspace list off the same
// control-plane links.
func servableWorkspaces(ctx context.Context, cp *tenancy.ControlPlane, links []tenancy.PortalLink, activeTenantID string) []workspaceView {
	out := []workspaceView{}
	for _, l := range links {
		t, err := cp.TenantByID(ctx, l.TenantID)
		if err != nil || !t.Servable() {
			continue
		}
		out = append(out, workspaceView{
			TenantID: l.TenantID,
			Name:     l.TenantName,
			Slug:     l.TenantSlug,
			Active:   l.TenantID == activeTenantID,
		})
	}
	return out
}

// Login authenticates a portal customer.
// Path: POST /api/portal/auth/login
//
// Returns a session for the first available workspace plus the full list, so a
// customer linked to several tenants can render the switcher immediately
// without a second round-trip.
func (h *PortalAuthOps) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.Email == "" || req.Password == "" {
		fail(w, http.StatusBadRequest, "email and password are required.")
		return
	}

	// Every failure below returns the same message. Distinguishing "no such
	// account" from "not a portal user" from "wrong password" would let the
	// endpoint be used to enumerate customer emails.
	identity, err := h.CP.IdentityByEmail(r.Context(), req.Email)
	if errors.Is(err, tenancy.ErrIdentityNotFound) {
		logSecurityEvent(r, "portal_login_failed", "email", req.Email, "reason", "unknown_identity")
		fail(w, http.StatusUnauthorized, "Invalid email or password.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Login failed.")
		return
	}
	if identity.PasswordHash == "" {
		fail(w, http.StatusUnauthorized, "Invalid email or password.")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(identity.PasswordHash), []byte(req.Password)); err != nil {
		logSecurityEvent(r, "portal_login_failed", "email", req.Email,
			"identity", identity.ID, "reason", "bad_password")
		fail(w, http.StatusUnauthorized, "Invalid email or password.")
		return
	}

	links, err := h.CP.PortalTenantsForIdentity(r.Context(), identity.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Login failed.")
		return
	}
	workspaces := servableWorkspaces(r.Context(), h.CP, links, "")
	if len(workspaces) == 0 {
		// Correct password, but no workspace is currently active for this
		// identity. Distinguish "never a portal customer here" (stay generic —
		// a staff member must not learn their password was accepted on this
		// endpoint) from "was one, but access is suspended/revoked" (say so —
		// once the password has already matched, "invalid email or password"
		// is actively misleading, not a security feature; see tryPortalLogin's
		// doc comment in tenant.go for the mirrored staff-side precedent).
		everLinked, everErr := h.CP.AnyPortalLinkExists(r.Context(), identity.ID)
		if everErr == nil && everLinked {
			logSecurityEvent(r, "portal_login_blocked_no_active_access", "identity", identity.ID)
			fail(w, http.StatusForbidden,
				"Your portal access is not currently active. Contact the business you work with to restore it.")
			return
		}
		logSecurityEvent(r, "portal_login_failed", "email", req.Email,
			"identity", identity.ID, "reason", "no_active_portal_workspace")
		fail(w, http.StatusUnauthorized, "Invalid email or password.")
		return
	}

	active := workspaces[0].TenantID
	workspaces[0].Active = true

	d := portalTokenDuration()
	token, err := generatePortalJWT(identity.ID, identity.Email, active, d)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to sign token.")
		return
	}
	refreshRaw, refreshExpiry, err := issueRefreshToken(r.Context(), h.CP, identity.ID)
	if err != nil {
		log.Printf("warn: failed to issue portal refresh token for identity %s: %v", identity.ID, err)
		refreshRaw = ""
	}
	if err := setAuthCookiesAt(w, token, d, refreshRaw, refreshExpiry, portalRefreshCookiePath); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to establish session.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"token":      token,
		"expiresAt":  time.Now().Add(d).UnixMilli(),
		"tenantId":   active,
		"workspaces": workspaces,
		"user": map[string]any{
			"id": identity.ID, "email": identity.Email, "fullName": identity.FullName,
		},
	})
}

// Workspaces lists the workspaces the caller may switch between.
// Path: GET /api/portal/workspaces
func (h *PortalAuthOps) Workspaces(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	links, err := h.CP.PortalTenantsForIdentity(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workspaces.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"workspaces": servableWorkspaces(r.Context(), h.CP, links, payload.TenantID),
	})
}

// SwitchWorkspace re-mints the session against a different workspace.
// Path: POST /api/portal/auth/switch-workspace
//
// Modelled on RBACOps.SwitchRole, with one structural difference: SwitchRole
// runs behind the tenancy resolver and reads the current tenant's pool, but a
// workspace switch CHANGES the tenant, so this handler must not depend on it.
// The target is authorized against the control plane instead.
func (h *PortalAuthOps) SwitchWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	var req struct {
		TenantID string `json:"tenantId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.TenantID == "" {
		fail(w, http.StatusBadRequest, "tenantId is required.")
		return
	}

	// The authorization check. Without it a portal session could mint itself a
	// token for any workspace whose id it could guess.
	allowed, err := h.CP.PortalLinkActive(r.Context(), payload.ID, req.TenantID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to switch workspace.")
		return
	}
	if !allowed {
		logSecurityEvent(r, "portal_workspace_switch_denied",
			"identity", payload.ID, "target_tenant", req.TenantID)
		fail(w, http.StatusForbidden, "You do not have access to this workspace.")
		return
	}

	tenant, err := h.CP.TenantByID(r.Context(), req.TenantID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to switch workspace.")
		return
	}
	if !tenant.Servable() {
		fail(w, http.StatusServiceUnavailable, "That workspace is not available right now.")
		return
	}

	d := portalTokenDuration()
	token, err := generatePortalJWT(payload.ID, payload.Email, req.TenantID, d)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to sign token.")
		return
	}
	// Refresh token is left untouched — it is identity-scoped, not workspace-
	// scoped, exactly as SwitchRole leaves it.
	if err := setAuthCookiesAt(w, token, d, "", time.Time{}, portalRefreshCookiePath); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to switch workspace.")
		return
	}

	logSecurityEvent(r, "portal_workspace_switched",
		"identity", payload.ID, "tenant", req.TenantID)

	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"token":         token,
		"expiresAt":     time.Now().Add(d).UnixMilli(),
		"tenantId":      req.TenantID,
		"workspaceName": tenant.DisplayName,
	})
}

// Refresh rotates a portal session.
// Path: POST /api/portal/auth/refresh
//
// Deliberately NOT TenantOps.RefreshSession. That handler re-mints using
// identities.tenant_id, which for a portal identity is only a home hint — using
// it here would silently teleport a customer back to their first workspace
// mid-session. This preserves the workspace the current token is on, and
// re-verifies access to it, so a revoked link cannot be refreshed back to life.
func (h *PortalAuthOps) Refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	cookie, err := r.Cookie("refresh_token")
	if err != nil || cookie.Value == "" {
		fail(w, http.StatusUnauthorized, "No refresh token provided.")
		return
	}
	// tenantId is optional: the workspace to resume. Omitted means "the first
	// one I am linked to", which is right for a single-workspace customer.
	var body struct {
		TenantID string `json:"tenantId"`
	}
	if r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	// Look up, then revoke before re-issuing — the same rotation order
	// TenantOps.RefreshSession uses.
	hash := tenancy.HashRefreshToken(cookie.Value)
	rec, err := h.CP.RefreshTokenByHash(r.Context(), hash)
	if errors.Is(err, tenancy.ErrRefreshTokenReused) {
		logSecurityEvent(r, "portal_refresh_token_reused")
		clearAuthCookies(w)
		fail(w, http.StatusUnauthorized, "Session invalid. Please sign in again.")
		return
	}
	if err != nil {
		clearAuthCookies(w)
		fail(w, http.StatusUnauthorized, "Session expired. Please sign in again.")
		return
	}
	if err := h.CP.RevokeRefreshToken(r.Context(), hash); err != nil {
		log.Printf("warn: portal refresh rotation: revoke old token: %v", err)
	}
	identityID := rec.IdentityID

	identity, err := h.CP.IdentityByID(r.Context(), identityID)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Session expired. Please sign in again.")
		return
	}

	// Which workspace to resume.
	//
	// It cannot come from the access token: that token is expired by definition
	// here, so RequireAuth does not run on this route and there is no request
	// context to read. It therefore comes from the caller — which is safe only
	// because of the PortalLinkActive check below. Without that check this would
	// be a workspace-hopping hole; with it, a caller can only resume a workspace
	// they were already granted.
	//
	// Falling back to identities.tenant_id here would be the bug this design
	// exists to avoid: for a portal identity that column is only a home hint, so
	// a refresh would silently move the customer to a different workspace.
	activeTenantID := body.TenantID
	if activeTenantID == "" {
		links, lerr := h.CP.PortalTenantsForIdentity(r.Context(), identityID)
		if lerr != nil || len(links) == 0 {
			fail(w, http.StatusUnauthorized, "Session expired. Please sign in again.")
			return
		}
		activeTenantID = links[0].TenantID
	}

	allowed, err := h.CP.PortalLinkActive(r.Context(), identityID, activeTenantID)
	if err != nil || !allowed {
		logSecurityEvent(r, "portal_refresh_denied", "identity", identityID, "tenant", activeTenantID)
		fail(w, http.StatusUnauthorized, "Session expired. Please sign in again.")
		return
	}

	d := portalTokenDuration()
	token, err := generatePortalJWT(identityID, identity.Email, activeTenantID, d)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to sign token.")
		return
	}
	refreshRaw, refreshExpiry, err := issueRefreshToken(r.Context(), h.CP, identityID)
	if err != nil {
		refreshRaw = ""
	}
	if err := setAuthCookiesAt(w, token, d, refreshRaw, refreshExpiry, portalRefreshCookiePath); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to refresh session.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "token": token,
		"expiresAt": time.Now().Add(d).UnixMilli(), "tenantId": activeTenantID,
	})
}

// Logout revokes the refresh token and clears the session cookies.
// Path: POST /api/portal/auth/logout
func (h *PortalAuthOps) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("refresh_token"); err == nil && cookie.Value != "" {
		_ = h.CP.RevokeRefreshToken(r.Context(), tenancy.HashRefreshToken(cookie.Value))
	}
	clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ---- credential setup / reset ----------------------------------------------

// GetInvite validates an invitation link without consuming it.
// Path: GET /api/portal/auth/invite/{token}
//
// Distinguishes expired from invalid so the UI can say "ask your contact to
// resend" rather than a dead end. That distinction is safe here: the token is
// unguessable, so revealing that a specific 64-hex-character string is an
// expired invite tells an attacker nothing they did not already have.
func (h *PortalAuthOps) GetInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		fail(w, http.StatusBadRequest, "Token is required.")
		return
	}
	invite, err := h.CP.PortalInviteByToken(r.Context(), token)
	if err != nil {
		fail(w, http.StatusBadRequest, "This invitation link is not valid.")
		return
	}
	switch {
	case invite.Status == tenancy.PortalInviteStatusAccepted:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "status": "accepted",
			"message": "This invitation has already been used. Try signing in instead.",
		})
		return
	case invite.Status == tenancy.PortalInviteStatusRevoked:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "status": "revoked",
			"message": "This invitation is no longer valid.",
		})
		return
	case invite.Expired():
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "status": "expired",
			"message": "This invitation has expired. Ask your contact to send a new one.",
		})
		return
	}

	tenant, err := h.CP.TenantByID(r.Context(), invite.TenantID)
	workspace := ""
	if err == nil {
		workspace = tenant.DisplayName
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "status": "pending",
		"email": invite.Email, "fullName": invite.FullName,
		"workspaceName": workspace, "expiresAt": invite.ExpiresAt,
	})
}

// AcceptInvite consumes an invitation and sets the initial password.
// Path: POST /api/portal/auth/accept-invite
func (h *PortalAuthOps) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.Token == "" {
		fail(w, http.StatusBadRequest, "Token is required.")
		return
	}
	if len(req.Password) < portalMinPasswordLen {
		fail(w, http.StatusBadRequest, "Password must be at least 8 characters.")
		return
	}

	invite, err := h.CP.PortalInviteByToken(r.Context(), req.Token)
	if err != nil {
		fail(w, http.StatusBadRequest, "This invitation link is not valid.")
		return
	}
	if !invite.Usable() {
		status := invite.Status
		if invite.Expired() {
			status = "expired"
		}
		logSecurityEvent(r, "portal_invite_unusable",
			"identity", invite.IdentityID, "status", status)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "status": status,
			"message": "This invitation is no longer valid. Ask your contact to send a new one.",
		})
		return
	}

	// The invite is only good if the access it was issued for still stands —
	// staff may have revoked it between sending and acceptance.
	active, err := h.CP.PortalLinkActive(r.Context(), invite.IdentityID, invite.TenantID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to accept the invitation.")
		return
	}
	if !active {
		logSecurityEvent(r, "portal_invite_access_revoked", "identity", invite.IdentityID)
		fail(w, http.StatusBadRequest, "This invitation is no longer valid.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to set password.")
		return
	}
	if err := h.CP.SetIdentityPassword(r.Context(), invite.IdentityID, string(hash)); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to set password.")
		return
	}
	// Close the invite last: if this fails the password is already set, and a
	// still-pending invite is harmless because the identity now has a password
	// (which is what the resend path refuses to overwrite).
	if err := h.CP.MarkPortalInviteAccepted(r.Context(), invite.ID); err != nil {
		log.Printf("warn: mark portal invite %s accepted: %v", invite.ID, err)
	}

	logSecurityEvent(r, "portal_invite_accepted", "identity", invite.IdentityID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ForgotPassword emails a reset link to a portal customer.
// Path: POST /api/portal/auth/forgot-password
//
// Always returns 200 regardless of whether the address exists, has portal
// access, or is a staff account, so the endpoint cannot be used to work out
// which emails have customer logins.
func (h *PortalAuthOps) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	ok := map[string]any{"success": true,
		"message": "If that email has portal access, a reset link has been sent."}

	if req.Email == "" {
		writeJSON(w, http.StatusOK, ok)
		return
	}
	identity, err := h.CP.IdentityByEmail(r.Context(), req.Email)
	if err != nil {
		writeJSON(w, http.StatusOK, ok)
		return
	}
	isPortal, err := h.CP.HasAnyPortalLink(r.Context(), identity.ID)
	if err != nil || !isPortal {
		writeJSON(w, http.StatusOK, ok)
		return
	}
	// A customer who has not accepted their invitation yet has no password to
	// reset. Staff resend the invitation instead; silently doing nothing here
	// keeps the response uniform.
	if identity.PasswordHash == "" {
		writeJSON(w, http.StatusOK, ok)
		return
	}

	token, err := randomToken()
	if err != nil {
		writeJSON(w, http.StatusOK, ok)
		return
	}
	if err := h.CP.SetIdentityPasswordSetupToken(r.Context(), identity.ID, token,
		time.Now().Add(time.Hour)); err != nil {
		writeJSON(w, http.StatusOK, ok)
		return
	}
	if err := services.SendPasswordResetEmail(r.Context(), identity.TenantID, identity.ID, identity.Email, identity.FullName,
		portalResetLink(token)); err != nil {
		log.Printf("warn: portal reset email to %s failed: %v", identity.Email, err)
	}
	writeJSON(w, http.StatusOK, ok)
}

// ResetPassword consumes a forgotten-password token.
// Path: POST /api/portal/auth/reset-password
//
// Separate from AcceptInvite: reset uses identities.password_reset_token (set
// by ForgotPassword), invitation uses the portal_invites token. Keeping them
// apart is why a staff invite token can never be redeemed on the portal.
func (h *PortalAuthOps) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.Token == "" {
		fail(w, http.StatusBadRequest, "Token is required.")
		return
	}
	if len(req.Password) < portalMinPasswordLen {
		fail(w, http.StatusBadRequest, "Password must be at least 8 characters.")
		return
	}
	identity, ok := h.identityForPortalToken(w, r, req.Token)
	if !ok {
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to reset password.")
		return
	}
	// SetIdentityPassword NULLs the token in the same statement, so the link is
	// single-use without a separate delete.
	if err := h.CP.SetIdentityPassword(r.Context(), identity.ID, string(hash)); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to reset password.")
		return
	}
	logSecurityEvent(r, "portal_password_reset", "identity", identity.ID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ValidateResetToken checks a forgotten-password link.
// Path: GET /api/portal/auth/reset-password/{token}
func (h *PortalAuthOps) ValidateResetToken(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		fail(w, http.StatusBadRequest, "Token is required.")
		return
	}
	identity, ok := h.identityForPortalToken(w, r, token)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "email": identity.Email, "fullName": identity.FullName,
	})
}

// ChangePassword updates the authenticated portal customer's password.
// Path: POST /api/portal/auth/change-password
func (h *PortalAuthOps) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if len(req.NewPassword) < portalMinPasswordLen {
		fail(w, http.StatusBadRequest, "Password must be at least 8 characters.")
		return
	}
	identity, err := h.CP.IdentityByID(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to change password.")
		return
	}
	if identity.PasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(identity.PasswordHash), []byte(req.CurrentPassword)) != nil {
		logSecurityEvent(r, "portal_change_password_failed", "identity", payload.ID)
		fail(w, http.StatusUnauthorized, "Current password is incorrect.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to change password.")
		return
	}
	if err := h.CP.SetIdentityPassword(r.Context(), payload.ID, string(hash)); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to change password.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// identityForPortalToken resolves a setup/reset token to its identity and
// asserts that identity is a portal customer.
//
// The assertion is the point: identities.password_reset_token is one column
// serving staff invites, staff password resets and portal setup alike. Without
// this check a staff member's setup token could be redeemed through the portal
// endpoint, creating a portal-shaped session for a staff identity.
func (h *PortalAuthOps) identityForPortalToken(w http.ResponseWriter, r *http.Request, token string) (*tenancy.Identity, bool) {
	identity, err := h.CP.IdentityByPasswordToken(r.Context(), token)
	if err != nil {
		fail(w, http.StatusBadRequest, "This link is invalid or has expired.")
		return nil, false
	}
	isPortal, err := h.CP.HasAnyPortalLink(r.Context(), identity.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to validate link.")
		return nil, false
	}
	if !isPortal {
		logSecurityEvent(r, "portal_token_for_non_portal_identity", "identity", identity.ID)
		fail(w, http.StatusBadRequest, "This link is invalid or has expired.")
		return nil, false
	}
	return identity, true
}

// portalInviteLink is where an invited customer sets their initial password.
//
// Points at the shared /accept-invite page (not /portal/accept-invite): the
// login surfaces are merged, so there is one accept-invite page for every
// identity kind, not a portal-specific one.
func portalInviteLink(token string) string {
	return frontendBase() + "/accept-invite?token=" + token
}

// inviteExpiryHours is the configured invite lifetime in hours, for display in
// the invitation email. Mirrors inviteExpiry()'s fallback chain so the number
// shown to the customer always matches the number enforced.
func inviteExpiryHours() int {
	if h := config.AppConfig.InviteExpiryHours; h > 0 {
		return h
	}
	return 24
}

// portalResetLink is where a customer resets a forgotten password.
//
// Points at the shared /reset-password page, same reasoning as
// portalInviteLink above — one reset-password page for every identity kind.
func portalResetLink(token string) string {
	return frontendBase() + "/reset-password?token=" + token
}
