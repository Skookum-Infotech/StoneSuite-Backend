package controllers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// userInviteExpiry is the TTL for workspace user invitations.
const userInviteExpiry = 48 * time.Hour

// userInviteLink builds the public frontend URL an invited user follows to set
// their password and activate their account.
func userInviteLink(token string) string {
	return frontendBase() + "/accept-invite?token=" + token
}

// UserOps groups all workspace user management handlers (tenant-scoped CRUD +
// public accept-invite flow). Deps are injected for testability.
type UserOps struct {
	CP     *tenancy.ControlPlane
	Router *tenancy.Router
}

// NewUserOps constructs the handler group.
func NewUserOps(cp *tenancy.ControlPlane, router *tenancy.Router) *UserOps {
	return &UserOps{CP: cp, Router: router}
}

// authorizeUser checks that the caller holds resource:action on the resolved
// tenant. Writes the error response and returns false on failure.
func (h *UserOps) authorizeUser(w http.ResponseWriter, r *http.Request, action authz.Action) (middleware.UserContextPayload, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return payload, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return payload, false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceUser, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return payload, false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" users.")
		return payload, false
	}
	return payload, true
}

// ============================================================================
// Tenant-scoped endpoints (require auth + tenancy resolver)
// ============================================================================

// ListUsers GET /api/tenant/users
// Returns all workspace members with their assigned roles.
func (h *UserOps) ListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeUser(w, r, authz.ActionRead); !ok {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	users, err := userstore.ListUsers(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list users.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "users": users})
}

// InviteUser POST /api/tenant/users/invite
// Creates a pending invite, stores it in the control plane, and emails the recipient.
//
// Edge cases handled:
//   - Caller cannot invite their own email.
//   - Email already a workspace member → 409.
//   - Pending (non-expired) invite already exists for email → 409.
//   - Expired invite for same email → superseded (new invite created).
//   - Email already registered to another tenant → 409.
//   - initialRoleId supplied but does not exist → 400.
func (h *UserOps) InviteUser(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.authorizeUser(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}

	var req struct {
		Email         string `json:"email"`
		FullName      string `json:"fullName"`
		InitialRoleID string `json:"initialRoleId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.FullName = strings.TrimSpace(req.FullName)
	if req.Email == "" {
		fail(w, http.StatusBadRequest, "email is required.")
		return
	}

	// Guard: caller cannot invite themselves.
	if strings.EqualFold(req.Email, payload.Email) {
		fail(w, http.StatusBadRequest, "You cannot invite yourself.")
		return
	}

	// Guard: email already a workspace member.
	if _, err := userstore.GetUserByEmail(r.Context(), pool, req.Email); err == nil {
		fail(w, http.StatusConflict, "A user with this email already belongs to this workspace.")
		return
	}

	// Guard: email already registered in another tenant's CP identity.
	if existingIdentity, err := h.CP.IdentityByEmail(r.Context(), req.Email); err == nil {
		if existingIdentity.TenantID != tenant.ID {
			fail(w, http.StatusConflict, "This email address is already registered to another workspace.")
			return
		}
		// Same tenant + identity but no user row → rare edge case (partial provisioning).
		// Allow invite to continue; accept-invite will find the existing identity.
	}

	// Guard: active pending invite already exists for this email.
	existing, err := h.CP.PendingUserInviteByEmail(r.Context(), tenant.ID, req.Email)
	if err == nil && existing != nil && time.Now().Before(existing.ExpiresAt) {
		fail(w, http.StatusConflict, "A pending invitation for this email already exists. Use resend to refresh it.")
		return
	}

	// If initialRoleId is specified, verify the role exists in the tenant DB.
	if req.InitialRoleID != "" {
		if _, err := authz.GetRole(r.Context(), pool, req.InitialRoleID); err != nil {
			if errors.Is(err, authz.ErrRoleNotFound) {
				fail(w, http.StatusBadRequest, "The specified role does not exist.")
				return
			}
			fail(w, http.StatusInternalServerError, "Failed to validate role.")
			return
		}
	}

	token, err := randomToken()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate invite token.")
		return
	}

	invite, err := h.CP.CreateUserInvite(
		r.Context(),
		tenant.ID, req.Email, req.FullName, req.InitialRoleID,
		token, payload.ID,
		time.Now().Add(userInviteExpiry),
	)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create invitation.")
		return
	}

	link := userInviteLink(token)
	if err := services.SendUserInviteEmail(req.Email, req.FullName, tenant.DisplayName, link); err != nil {
		log.Printf("user invite email to %s failed (invite still valid): %v", req.Email, err)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"success":         true,
		"message":         "Invitation sent.",
		"inviteId":        invite.ID,
		"inviteLink":      link, // returned for dev/debug; omit in production UI
	})
}

// GetUser GET /api/tenant/users/{id}
// Returns a single user profile with their assigned roles.
func (h *UserOps) GetUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeUser(w, r, authz.ActionRead); !ok {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		fail(w, http.StatusBadRequest, "Missing user id.")
		return
	}
	u, err := userstore.GetUserByID(r.Context(), pool, id)
	if errors.Is(err, userstore.ErrUserNotFound) {
		fail(w, http.StatusNotFound, "User not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load user.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "user": u})
}

// UpdateUser PATCH /api/tenant/users/{id}
// Updates a user's full name and/or status.
//
// Status transitions allowed by callers with user:update:
//   - active  → suspended (suspend access without deactivating)
//   - suspended → active  (restore access)
//
// The 'disabled' (soft-delete) status is set only via DELETE /api/tenant/users/{id}.
// Attempting to set status='disabled' via PATCH returns 400.
//
// Edge cases:
//   - Suspending the last active super_admin is blocked.
//   - Caller can update their own profile (name only); status change requires user:update.
func (h *UserOps) UpdateUser(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.authorizeUser(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		fail(w, http.StatusBadRequest, "Missing user id.")
		return
	}

	var req struct {
		FullName string `json:"fullName"`
		Status   string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	req.Status = strings.TrimSpace(req.Status)
	if req.Status == "disabled" {
		fail(w, http.StatusBadRequest, "Use DELETE /api/tenant/users/{id} to deactivate a user.")
		return
	}
	if req.Status != "" && req.Status != "active" && req.Status != "suspended" {
		fail(w, http.StatusBadRequest, "status must be 'active' or 'suspended'.")
		return
	}

	// Suspending the last super_admin is not allowed.
	if req.Status == "suspended" {
		target, err := userstore.GetUserByID(r.Context(), pool, id)
		if errors.Is(err, userstore.ErrUserNotFound) {
			fail(w, http.StatusNotFound, "User not found.")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load user.")
			return
		}
		if target.Status != "suspended" {
			isSA, err := userstore.IsSuperAdmin(r.Context(), pool, id)
			if err != nil {
				fail(w, http.StatusInternalServerError, "Failed to check admin status.")
				return
			}
			if isSA {
				count, err := userstore.CountActiveSuperAdmins(r.Context(), pool)
				if err != nil {
					fail(w, http.StatusInternalServerError, "Failed to count super admins.")
					return
				}
				if count <= 1 {
					fail(w, http.StatusConflict, "Cannot suspend the last active super admin.")
					return
				}
			}
		}
		_ = payload // suppress unused warning; payload used for future audit logging
	}

	u, err := userstore.UpdateUser(r.Context(), pool, id, req.FullName, req.Status)
	if errors.Is(err, userstore.ErrUserNotFound) {
		fail(w, http.StatusNotFound, "User not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update user.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "user": u})
}

// DeactivateUser DELETE /api/tenant/users/{id}
// Soft-deletes a user (sets status = 'disabled'). Role assignments are preserved.
//
// Edge cases:
//   - Last active super_admin cannot be deactivated.
//   - Caller cannot deactivate themselves.
func (h *UserOps) DeactivateUser(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.authorizeUser(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		fail(w, http.StatusBadRequest, "Missing user id.")
		return
	}

	// Guard: caller cannot deactivate their own account.
	if payload.UserID != "" && payload.UserID == id {
		fail(w, http.StatusBadRequest, "You cannot deactivate your own account.")
		return
	}

	// Guard: last active super_admin cannot be removed.
	isSA, err := userstore.IsSuperAdmin(r.Context(), pool, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to check admin status.")
		return
	}
	if isSA {
		count, err := userstore.CountActiveSuperAdmins(r.Context(), pool)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to count super admins.")
			return
		}
		if count <= 1 {
			fail(w, http.StatusConflict, "Cannot deactivate the last active super admin.")
			return
		}
	}

	if err := userstore.DeactivateUser(r.Context(), pool, id); err != nil {
		if errors.Is(err, userstore.ErrUserNotFound) {
			fail(w, http.StatusNotFound, "User not found.")
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to deactivate user.")
		return
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "User deactivated."})
}

// ListInvites GET /api/tenant/invites
// Returns all user invites for the resolved tenant (all statuses).
func (h *UserOps) ListInvites(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeUser(w, r, authz.ActionRead); !ok {
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	invites, err := h.CP.ListUserInvitesByTenant(r.Context(), tenant.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list invites.")
		return
	}
	if invites == nil {
		invites = []tenancy.UserInvite{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "invites": invites})
}

// ResendInvite POST /api/tenant/invites/{id}/resend
// Regenerates the invite token and resends the email.
//
// Edge cases:
//   - Invite not found → 404.
//   - Invite already accepted → 400.
//   - Invite not belonging to this tenant → 404 (tenant isolation).
func (h *UserOps) ResendInvite(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.authorizeUser(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	_ = payload
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		fail(w, http.StatusBadRequest, "Missing invite id.")
		return
	}

	invite, err := h.CP.UserInviteByID(r.Context(), id)
	if errors.Is(err, tenancy.ErrUserInviteNotFound) {
		fail(w, http.StatusNotFound, "Invite not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load invite.")
		return
	}
	// Tenant isolation: only operate on this tenant's invites.
	if invite.TenantID != tenant.ID {
		fail(w, http.StatusNotFound, "Invite not found.")
		return
	}
	if invite.Status == "accepted" {
		fail(w, http.StatusBadRequest, "This invitation has already been accepted.")
		return
	}
	if invite.Status == "revoked" {
		fail(w, http.StatusBadRequest, "This invitation has been revoked and cannot be resent.")
		return
	}

	token, err := randomToken()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate token.")
		return
	}
	refreshed, err := h.CP.RefreshUserInvite(r.Context(), id, token, time.Now().Add(userInviteExpiry))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to refresh invite.")
		return
	}

	link := userInviteLink(token)
	if err := services.SendUserInviteEmail(refreshed.Email, refreshed.FullName, tenant.DisplayName, link); err != nil {
		log.Printf("resend user invite email to %s failed (link still valid): %v", refreshed.Email, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"message":    "Invitation resent.",
		"inviteLink": link,
	})
}

// RevokeInvite DELETE /api/tenant/invites/{id}
// Cancels a pending invite. Accepted invites cannot be revoked.
func (h *UserOps) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeUser(w, r, authz.ActionDelete); !ok {
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		fail(w, http.StatusBadRequest, "Missing invite id.")
		return
	}

	// Verify the invite belongs to this tenant before revoking.
	invite, err := h.CP.UserInviteByID(r.Context(), id)
	if errors.Is(err, tenancy.ErrUserInviteNotFound) {
		fail(w, http.StatusNotFound, "Invite not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load invite.")
		return
	}
	if invite.TenantID != tenant.ID {
		fail(w, http.StatusNotFound, "Invite not found.")
		return
	}
	if invite.Status == "accepted" {
		fail(w, http.StatusBadRequest, "Accepted invites cannot be revoked.")
		return
	}

	if err := h.CP.RevokeUserInvite(r.Context(), id); err != nil {
		if errors.Is(err, tenancy.ErrUserInviteNotFound) {
			fail(w, http.StatusNotFound, "Invite not found or already closed.")
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to revoke invite.")
		return
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Invitation revoked."})
}

// ============================================================================
// Public endpoints (no auth — accept-invite flow)
// ============================================================================

// GetUserInvite GET /api/onboarding/user-invite/{token}
// Validates a user invite token and returns the invited email + workspace name.
// Used by the frontend to pre-populate the accept-invite form.
//
// Edge cases:
//   - Token not found → 404.
//   - Token already accepted → 400 (with status in body so frontend can redirect).
//   - Token expired → 400.
//   - Token revoked → 400.
func (h *UserOps) GetUserInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		fail(w, http.StatusBadRequest, "Missing invite token.")
		return
	}
	invite, err := h.CP.UserInviteByToken(r.Context(), token)
	if errors.Is(err, tenancy.ErrUserInviteNotFound) {
		fail(w, http.StatusNotFound, "Invite not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load invite.")
		return
	}

	if invite.Status == "accepted" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"status":  "accepted",
			"message": "This invitation has already been accepted. Please sign in.",
		})
		return
	}
	if invite.Status == "revoked" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"status":  "revoked",
			"message": "This invitation has been revoked. Contact your workspace admin.",
		})
		return
	}
	if time.Now().After(invite.ExpiresAt) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"status":  "expired",
			"message": "This invitation has expired. Ask your workspace admin to resend it.",
		})
		return
	}

	tenant, err := h.CP.TenantByID(r.Context(), invite.TenantID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workspace details.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"email":         invite.Email,
		"fullName":      invite.FullName,
		"workspaceName": tenant.DisplayName,
		"expiresAt":     invite.ExpiresAt,
	})
}

// AcceptUserInvite POST /api/onboarding/user-invite/accept
// Accepts a workspace invite: creates/links a CP identity, creates a tenant
// user row, assigns the initial role if specified, and marks the invite accepted.
//
// Request body: { token, password, fullName }
//   - fullName may override the name supplied at invite time.
//
// Edge cases:
//   - Token expired/accepted/revoked → 400.
//   - Password too short → 400.
//   - Email already has identity in ANOTHER tenant → 409.
//   - Email already has identity in SAME tenant → reuse identity, create user row only.
//   - User row already exists for this identity → idempotent (return success).
//   - Tenant not provisioned → 503.
//   - Initial role no longer exists (deleted between invite and accept) → user created without role.
func (h *UserOps) AcceptUserInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}

	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		FullName string `json:"fullName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	req.FullName = strings.TrimSpace(req.FullName)
	if req.Token == "" {
		fail(w, http.StatusBadRequest, "token is required.")
		return
	}
	if len(req.Password) < 8 {
		fail(w, http.StatusBadRequest, "Password must be at least 8 characters.")
		return
	}

	// Load and validate the invite.
	invite, err := h.CP.UserInviteByToken(r.Context(), req.Token)
	if errors.Is(err, tenancy.ErrUserInviteNotFound) {
		fail(w, http.StatusNotFound, "Invite not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load invite.")
		return
	}
	switch invite.Status {
	case "accepted":
		fail(w, http.StatusBadRequest, "This invitation has already been accepted.")
		return
	case "revoked":
		fail(w, http.StatusBadRequest, "This invitation has been revoked.")
		return
	}
	if time.Now().After(invite.ExpiresAt) {
		fail(w, http.StatusBadRequest, "This invitation has expired. Ask your workspace admin to resend it.")
		return
	}

	// Resolve the tenant.
	tenant, err := h.CP.TenantByID(r.Context(), invite.TenantID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workspace.")
		return
	}
	if !tenant.Servable() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success": false,
			"message": "The workspace is still being provisioned. Please try again shortly.",
		})
		return
	}

	pool, err := h.Router.PoolFor(r.Context(), tenant)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to connect to workspace database.")
		return
	}

	// Resolve the display name: request body overrides invite-time name.
	fullName := invite.FullName
	if req.FullName != "" {
		fullName = req.FullName
	}

	// Hash the password.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to hash password.")
		return
	}

	// Find or create CP identity.
	identity, err := h.CP.IdentityByEmail(r.Context(), invite.Email)
	if errors.Is(err, tenancy.ErrIdentityNotFound) {
		// New identity — create it for this tenant.
		identity, err = h.CP.CreateIdentity(r.Context(), invite.TenantID, invite.Email, string(hash), fullName, true)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to create account.")
			return
		}
	} else if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to look up account.")
		return
	} else {
		// Identity exists — ensure it belongs to THIS tenant.
		if identity.TenantID != invite.TenantID {
			fail(w, http.StatusConflict,
				"This email is already registered to a different workspace. Please contact support.")
			return
		}
		// Update the password and mark verified.
		if err := h.CP.SetIdentityPassword(r.Context(), identity.ID, string(hash)); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to update account.")
			return
		}
	}

	// Find or create the tenant-local user row.
	user, err := userstore.GetUserByIdentityID(r.Context(), pool, identity.ID)
	if errors.Is(err, userstore.ErrUserNotFound) {
		user, err = userstore.CreateUser(r.Context(), pool, identity.ID, invite.Email, fullName, "active")
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to create workspace profile.")
			return
		}
	} else if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workspace profile.")
		return
	} else {
		// User row already exists — ensure they are active (re-activation edge case).
		if user.Status != "active" {
			if _, err := userstore.UpdateUser(r.Context(), pool, user.ID, "", "active"); err != nil {
				log.Printf("accept invite: failed to reactivate user %s: %v", user.ID, err)
			}
		}
	}

	// Assign initial role if the invite specified one and it still exists.
	if invite.InitialRoleID != "" {
		if _, err := authz.GetRole(r.Context(), pool, invite.InitialRoleID); err == nil {
			if assignErr := authz.AssignRole(r.Context(), pool, user.ID, invite.InitialRoleID); assignErr != nil {
				log.Printf("accept invite: failed to assign initial role %s to user %s: %v",
					invite.InitialRoleID, user.ID, assignErr)
			}
		} else {
			log.Printf("accept invite: initial role %s no longer exists, skipping assignment", invite.InitialRoleID)
		}
	}

	// Mark the invite accepted (idempotent if called twice).
	if err := h.CP.MarkUserInviteAccepted(r.Context(), invite.ID); err != nil {
		log.Printf("accept invite: failed to mark invite accepted (non-fatal): %v", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Account activated. You can now sign in.",
		"email":   invite.Email,
	})
}
