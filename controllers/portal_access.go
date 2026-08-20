package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/portal"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
	"stonesuite-backend/workflow"
)

// PortalAccessOps is the staff-facing side of the customer portal: granting,
// listing and withdrawing customer logins.
//
// Gated on authz.ResourcePortalAccess rather than customer:update, because
// creating one of these mints an external credential into the workspace. That
// is a security decision, and a tenant should be able to let sales staff edit
// customers without also letting them create outside logins.
type PortalAccessOps struct {
	CP     *tenancy.ControlPlane
	Router *tenancy.Router
}

// NewPortalAccessOps constructs the handler group.
func NewPortalAccessOps(cp *tenancy.ControlPlane, router *tenancy.Router) *PortalAccessOps {
	return &PortalAccessOps{CP: cp, Router: router}
}

// authPortalAccess checks the caller's permission and returns the tenant pool.
// Follows the per-module auth<Module> convention used across controllers.
func (h *PortalAccessOps) authPortalAccess(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourcePortalAccess, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied", "identity", payload.ID,
			"resource", string(authz.ResourcePortalAccess), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to manage customer portal access.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// customerForPortal resolves the {customerUuid} path value and confirms the
// customer may be given portal access at all.
func (h *PortalAccessOps) customerForPortal(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, requireEligible bool) (int, string, bool) {
	uuid := r.PathValue("customerUuid")
	if uuid == "" {
		fail(w, http.StatusBadRequest, "customerUuid is required.")
		return 0, "", false
	}
	id, name, err := portal.CustomerEligible(r.Context(), pool, uuid)
	switch {
	case errors.Is(err, portal.ErrPortalUserNotFound):
		fail(w, http.StatusNotFound, "Customer not found.")
		return 0, "", false
	case errors.Is(err, portal.ErrCustomerNotEligible):
		if !requireEligible {
			// Listing and revoking must keep working for a customer that has
			// since been transitioned or un-approved — otherwise access could
			// be granted and then never withdrawn.
			lid, lname, lerr := portal.CustomerIDByUUID(r.Context(), pool, uuid)
			if lerr != nil {
				fail(w, http.StatusNotFound, "Customer not found.")
				return 0, "", false
			}
			return lid, lname, true
		}
		fail(w, http.StatusConflict,
			"Portal access can only be given to an approved customer. Approve this record first.")
		return 0, "", false
	case err != nil:
		fail(w, http.StatusInternalServerError, "Failed to load customer.")
		return 0, "", false
	}
	return id, name, true
}

// CreatePortalUser grants a customer contact a portal login.
// Path: POST /api/tenant/customers/{customerUuid}/portal-users
func (h *PortalAccessOps) CreatePortalUser(w http.ResponseWriter, r *http.Request) {
	pool, actorIdentityID, ok := h.authPortalAccess(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	customerID, customerName, ok := h.customerForPortal(w, r, pool, true)
	if !ok {
		return
	}

	var req struct {
		Email    string `json:"email"`
		FullName string `json:"fullName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		fail(w, http.StatusBadRequest, "A valid email is required.")
		return
	}

	// Find or create the control-plane identity.
	identity, err := h.CP.IdentityByEmail(r.Context(), req.Email)
	switch {
	case errors.Is(err, tenancy.ErrIdentityNotFound):
		// Password-less identity; the setup link below is what gives it one.
		// tenant.ID is stored as the home hint — identity_tenants is the
		// authority on which workspaces this login may enter.
		identity, err = h.CP.CreateIdentity(r.Context(), tenant.ID, req.Email, "", req.FullName, false)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to create portal login.")
			return
		}
	case err != nil:
		fail(w, http.StatusInternalServerError, "Failed to create portal login.")
		return
	default:
		// The email already has an identity. Refuse if it belongs to a staff
		// member anywhere: one address must not be both an employee login and
		// a customer login, or the two surfaces would share a password and a
		// reset token with different trust levels.
		staff, serr := h.identityIsStaff(r, identity)
		if serr != nil {
			fail(w, http.StatusInternalServerError, "Failed to create portal login.")
			return
		}
		if staff {
			logSecurityEvent(r, "portal_access_refused_staff_email",
				"identity", identity.ID, "actor", actorIdentityID)
			fail(w, http.StatusConflict,
				"That email already belongs to a workspace user and cannot be used for portal access.")
			return
		}
	}

	if _, err := h.CP.CreatePortalLink(r.Context(), identity.ID, tenant.ID); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create portal login.")
		return
	}

	actorEmp := employeeIDOrNil(r, pool, actorIdentityID)
	user, err := portal.CreateUser(r.Context(), pool, identity.ID, customerID,
		req.Email, req.FullName, actorEmp)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create portal login.")
		return
	}

	// Issue the invitation. Only when the identity has no password yet — an
	// existing portal customer at another workspace already has credentials and
	// just needs the new workspace linked, not a new invite.
	var invite *tenancy.PortalInvite
	if identity.PasswordHash == "" {
		invite, err = h.issueInvite(r, tenant, identity.ID, req.Email, req.FullName,
			r.PathValue("customerUuid"), actorIdentityID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to send the portal invitation.")
			return
		}
	}

	logSecurityEvent(r, "portal_access_granted", "actor", actorIdentityID,
		"identity", identity.ID, "customer", customerName, "tenant", tenant.ID)

	writeJSON(w, http.StatusCreated, map[string]any{
		"success":    true,
		"portalUser": portalUserView(user, invite),
	})
}

// issueInvite creates or refreshes a pending invite and emails the link.
//
// The link is sent by email only and is deliberately never returned in an API
// response: holding portal_access:create should not by itself yield a working
// credential-setting link for someone else's address.
//
// An email failure is logged, not fatal — access has been granted either way,
// and staff can resend. Returning 500 here would leave the caller unsure
// whether the grant happened.
func (h *PortalAccessOps) issueInvite(r *http.Request, tenant *tenancy.Tenant,
	identityID, email, fullName, customerUUID, actorIdentityID string) (*tenancy.PortalInvite, error) {
	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("generate portal invite token: %w", err)
	}
	expiresAt := inviteExpiry(0) // INVITE_EXPIRY_HOURS, same config as every other invite

	// Refresh an outstanding invite rather than stacking a second one — the
	// live-pending unique index would reject the insert anyway.
	var invite *tenancy.PortalInvite
	existing, lerr := h.CP.PendingPortalInviteByEmail(r.Context(), tenant.ID, email)
	switch {
	case lerr == nil:
		invite, err = h.CP.RefreshPortalInvite(r.Context(), existing.ID, token, expiresAt)
	case errors.Is(lerr, tenancy.ErrPortalInviteNotFound):
		invite, err = h.CP.CreatePortalInvite(r.Context(), tenant.ID, identityID,
			email, fullName, customerUUID, token, actorIdentityID, expiresAt)
	default:
		return nil, fmt.Errorf("look up pending portal invite: %w", lerr)
	}
	if err != nil {
		return nil, fmt.Errorf("issue portal invite: %w", err)
	}

	if merr := services.SendPortalInviteEmail(email, fullName, tenant.DisplayName,
		portalInviteLink(token), inviteExpiryHours()); merr != nil {
		log.Printf("warn: portal invite email to %s failed: %v", email, merr)
	}
	return invite, nil
}

// ResendPortalInvite re-issues and re-sends an invitation.
// Path: POST /api/tenant/customers/{customerUuid}/portal-users/{id}/resend
//
// Mints a NEW token, so the previous link stops working — a resend doubles as
// the way to invalidate a link that may have been forwarded or leaked.
func (h *PortalAccessOps) ResendPortalInvite(w http.ResponseWriter, r *http.Request) {
	// Resending is granting access again, so it takes the create permission
	// rather than read: it produces a working credential link.
	pool, actorIdentityID, ok := h.authPortalAccess(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	customerID, _, ok := h.customerForPortal(w, r, pool, false)
	if !ok {
		return
	}
	user, err := portal.GetUser(r.Context(), pool, customerID, r.PathValue("id"))
	if errors.Is(err, portal.ErrPortalUserNotFound) {
		fail(w, http.StatusNotFound, "Portal login not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load portal login.")
		return
	}
	if user.Status != portal.StatusActive {
		fail(w, http.StatusConflict, "That portal login has been revoked. Grant access again instead.")
		return
	}

	// Nothing to resend once they have a password — that is a reset, which the
	// customer starts themselves from the portal sign-in page.
	identity, err := h.CP.IdentityByID(r.Context(), user.IdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load portal login.")
		return
	}
	if identity.PasswordHash != "" {
		fail(w, http.StatusConflict,
			"That customer has already set a password. Ask them to use \"forgot password\" instead.")
		return
	}

	invite, err := h.issueInvite(r, tenant, user.IdentityID, user.Email, user.FullName,
		r.PathValue("customerUuid"), actorIdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to resend the portal invitation.")
		return
	}

	logSecurityEvent(r, "portal_invite_resent", "actor", actorIdentityID,
		"identity", user.IdentityID, "tenant", tenant.ID)

	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"portalUser": portalUserView(user, invite),
	})
}

// portalUserView renders a portal login plus its invitation state for staff.
//
// inviteStatus is what the UI needs to decide whether to offer "resend":
// pending / expired / accepted / none.
func portalUserView(u *portal.User, invite *tenancy.PortalInvite) map[string]any {
	view := map[string]any{
		"id": u.ID, "email": u.Email, "fullName": u.FullName,
		"status": u.Status, "createdAt": u.CreatedAt, "inviteStatus": "none",
	}
	if u.RevokedAt != nil {
		view["revokedAt"] = u.RevokedAt
	}
	if invite == nil {
		return view
	}
	switch {
	case invite.Expired():
		view["inviteStatus"] = "expired"
	case invite.Status == tenancy.PortalInviteStatusPending:
		view["inviteStatus"] = "pending"
	default:
		view["inviteStatus"] = invite.Status
	}
	view["inviteExpiresAt"] = invite.ExpiresAt
	return view
}

// ListPortalUsers lists a customer's portal logins, revoked ones included so
// the access history stays visible.
// Path: GET /api/tenant/customers/{customerUuid}/portal-users
func (h *PortalAccessOps) ListPortalUsers(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authPortalAccess(w, r, authz.ActionRead)
	if !ok {
		return
	}
	customerID, _, ok := h.customerForPortal(w, r, pool, false)
	if !ok {
		return
	}
	users, err := portal.ListUsersForCustomer(r.Context(), pool, customerID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list portal logins.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}

	// Attach each login's invitation state so the UI can show "invited",
	// "expired" (with a resend button) or nothing at all, without the frontend
	// having to infer it.
	views := make([]map[string]any, 0, len(users))
	for i := range users {
		u := users[i]
		invite, ierr := h.CP.LatestPortalInviteForIdentity(r.Context(), tenant.ID, u.IdentityID)
		if ierr != nil {
			// No invite on record is normal for a customer who already had a
			// login at another workspace. Render the row without invite state.
			invite = nil
		}
		views = append(views, portalUserView(&u, invite))
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "portalUsers": views})
}

// RevokePortalUser withdraws one portal login.
// Path: DELETE /api/tenant/customers/{customerUuid}/portal-users/{id}
//
// Revokes in both databases and kills live sessions: the tenant-side row (which
// the per-request session resolution reads), the control-plane link (which the
// workspace switcher and login read), and every refresh token for that identity
// so an active session cannot be rotated back to life.
func (h *PortalAccessOps) RevokePortalUser(w http.ResponseWriter, r *http.Request) {
	pool, actorIdentityID, ok := h.authPortalAccess(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	customerID, _, ok := h.customerForPortal(w, r, pool, false)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		fail(w, http.StatusBadRequest, "Portal user id is required.")
		return
	}

	user, err := portal.GetUser(r.Context(), pool, customerID, id)
	if errors.Is(err, portal.ErrPortalUserNotFound) {
		fail(w, http.StatusNotFound, "Portal login not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load portal login.")
		return
	}

	actorEmp := employeeIDOrNil(r, pool, actorIdentityID)
	if err := portal.RevokeUser(r.Context(), pool, customerID, id, actorEmp); err != nil {
		if errors.Is(err, portal.ErrPortalUserNotFound) {
			fail(w, http.StatusNotFound, "Portal login not found.")
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to revoke portal login.")
		return
	}
	if err := h.CP.RevokePortalLink(r.Context(), user.IdentityID, tenant.ID); err != nil &&
		!errors.Is(err, tenancy.ErrPortalLinkNotFound) {
		log.Printf("warn: revoke portal link for identity %s: %v", user.IdentityID, err)
	}
	// Cancel any invitation still in flight. AcceptInvite already re-checks the
	// link, so an un-cancelled invite could not be redeemed — but leaving it
	// "pending" would misreport the customer's state to staff.
	if err := h.CP.RevokePortalInvitesForIdentity(r.Context(), tenant.ID, user.IdentityID); err != nil {
		log.Printf("warn: revoke portal invites for identity %s: %v", user.IdentityID, err)
	}
	if err := h.CP.RevokeAllRefreshTokens(r.Context(), user.IdentityID); err != nil {
		log.Printf("warn: revoke refresh tokens for identity %s: %v", user.IdentityID, err)
	}

	logSecurityEvent(r, "portal_access_revoked", "actor", actorIdentityID,
		"identity", user.IdentityID, "tenant", tenant.ID)

	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// identityIsStaff reports whether an identity is a workspace member anywhere.
//
// Checked against the identity's home tenant, which is where a staff identity's
// users row necessarily lives.
func (h *PortalAccessOps) identityIsStaff(r *http.Request, identity *tenancy.Identity) (bool, error) {
	if identity.TenantID == "" {
		return false, nil
	}
	t, err := h.CP.TenantByID(r.Context(), identity.TenantID)
	if err != nil || !t.Servable() {
		// Cannot confirm; treat as not staff rather than blocking the grant.
		return false, nil
	}
	pool, err := h.Router.PoolFor(r.Context(), t)
	if err != nil {
		return false, nil
	}
	_, err = userstore.GetUserByIdentityID(r.Context(), pool, identity.ID)
	if errors.Is(err, userstore.ErrUserNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// employeeIDOrNil resolves the acting staff member's employee id for audit
// columns. Nil when the caller has no employee row — the audit column is
// nullable and a missing attribution must not block the operation.
func employeeIDOrNil(r *http.Request, pool *pgxpool.Pool, identityID string) *int {
	if empID, found := workflow.EmployeeIDByIdentity(r.Context(), pool, identityID); found {
		return &empID
	}
	return nil
}
