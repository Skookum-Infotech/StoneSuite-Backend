package controllers

import (
	"context"
	"net/http"

	"stonesuite-backend/tenancy"
)

// isPlatformAdminIdentity reports whether the identity holds platform-admin
// powers. A lookup failure counts as "no": this only ever decides whether to
// *skip* a refusal, so failing closed is the safe direction.
func (h *TenantOps) isPlatformAdminIdentity(ctx context.Context, identityID string) bool {
	ok, err := h.CP.IsPlatformAdmin(ctx, identityID)
	return err == nil && ok
}

// refuseUnservableLogin handles a password login whose home tenant cannot be
// served (suspended, deleted, not yet provisioned, under maintenance). It runs
// only after the password has been verified, so a wrong password never reveals
// a workspace's state. It returns true once it has written the response;
// false means the caller carries on, which happens only for a platform admin.
//
// Two identities are deliberately not refused outright:
//   - A platform admin keeps access to the platform console whatever state their
//     home workspace is in (the console's routes need no tenant database), so an
//     unhealthy workspace can never lock out the operator who has to repair it.
//   - A portal customer's home tenant is just one of their links; suspending
//     that business must not lock them out of the others, so the portal login
//     (which only offers servable workspaces) gets its turn first.
func (h *TenantOps) refuseUnservableLogin(w http.ResponseWriter, r *http.Request, identity *tenancy.Identity, tenant *tenancy.Tenant) bool {
	if h.isPlatformAdminIdentity(r.Context(), identity.ID) {
		return false
	}
	if h.tryPortalLogin(w, r, identity) {
		return true
	}
	logSecurityEvent(r, "login_blocked", "identity", identity.ID, "tenant", tenant.ID,
		"status", tenant.Status, "reason", "unservable_tenant")
	code, msg := tenancy.UnservableReason(tenant)
	failWithCode(w, http.StatusForbidden, msg, code)
	return true
}

// refuseUnservableRefresh is the refresh-token counterpart of
// refuseUnservableLogin: a suspended or deleted workspace's sessions must not be
// extended, or its users would stay signed in for as long as they kept
// refreshing. The cookies are cleared so the browser stops retrying, and the
// same platform-admin exemption applies. (A portal customer's session refreshes
// through the portal endpoint, which already checks the workspace.)
func (h *TenantOps) refuseUnservableRefresh(w http.ResponseWriter, r *http.Request, identity *tenancy.Identity, tenant *tenancy.Tenant) bool {
	if h.isPlatformAdminIdentity(r.Context(), identity.ID) {
		return false
	}
	logSecurityEvent(r, "refresh_blocked", "identity", identity.ID, "tenant", tenant.ID,
		"status", tenant.Status, "reason", "unservable_tenant")
	clearAuthCookies(w)
	code, msg := tenancy.UnservableReason(tenant)
	failWithCode(w, http.StatusForbidden, msg, code)
	return true
}
