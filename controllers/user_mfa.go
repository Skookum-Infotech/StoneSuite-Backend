package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"stonesuite-backend/authz"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// A super admin turning two-step verification on or off for other members, one
// or many at a time, from Configuration > Users. Turning it on needs no setup
// step: the member's next sign-in asks for the code emailed to their address,
// and they get a notice that an administrator changed it.

const (
	// maxBulkMFAUsers caps one request; the Users page never selects more.
	maxBulkMFAUsers = 200
	// mfaNoticeConcurrency bounds how many notice emails go out at once.
	mfaNoticeConcurrency = 8
)

// Outcomes reported per member.
const (
	mfaResultChanged   = "changed"
	mfaResultUnchanged = "unchanged"
	mfaResultSkipped   = "skipped"
	mfaResultFailed    = "failed"
)

// Why a member was skipped.
const (
	mfaSkipNotFound    = "not_found"
	mfaSkipDeactivated = "deactivated"
	mfaSkipSelf        = "self"
	mfaSkipUnavailable = mfaUnavailable
)

var userIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// userWithMFA is a workspace member plus their two-step setting, which lives on
// the control-plane identity rather than the tenant user row.
type userWithMFA struct {
	userstore.User
	MFAEnabled   bool `json:"mfaEnabled"`
	MFAAvailable bool `json:"mfaAvailable"`
}

// withMFAStates attaches each member's two-step setting. A member whose
// identity can't be found is reported as unavailable rather than as "off".
func (h *UserOps) withMFAStates(r *http.Request, tenantID string, users []userstore.User) ([]userWithMFA, error) {
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.IdentityID)
	}
	states, err := h.CP.IdentityMFAStates(r.Context(), tenantID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]userWithMFA, 0, len(users))
	for _, u := range users {
		s := states[u.IdentityID]
		out = append(out, userWithMFA{User: u, MFAEnabled: s.Enabled, MFAAvailable: s.Available})
	}
	return out, nil
}

// mfaChangeResult is one member's outcome in a SetUsersMFA response.
type mfaChangeResult struct {
	UserID    string `json:"userId"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	EmailSent *bool  `json:"emailSent,omitempty"`
}

// SetUsersMFA turns two-step verification on or off for the listed members.
// Super admins only. One member being skipped (already in that state, SSO-only,
// deactivated, the caller themselves) never blocks the rest.
// Path: POST /api/tenant/users/mfa
func (h *UserOps) SetUsersMFA(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.authorizeUser(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	isSA, err := userstore.IsSuperAdmin(r.Context(), pool, payload.UserID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to check admin status.")
		return
	}
	if !isSA {
		fail(w, http.StatusForbidden, "Only a super admin can change two-step verification for other users.")
		return
	}

	var req struct {
		UserIDs []string `json:"userIds"`
		Enabled *bool    `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	ids, msg := cleanUserIDs(req.UserIDs)
	if msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	enabled := *req.Enabled

	// Load every target and its setting up front: two queries, not two per member.
	targets := make(map[string]*userstore.User, len(ids))
	identityIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		u, err := userstore.GetUserByID(r.Context(), pool, id)
		if errors.Is(err, userstore.ErrUserNotFound) {
			continue
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load users.")
			return
		}
		targets[id] = u
		identityIDs = append(identityIDs, u.IdentityID)
	}
	states, err := h.CP.IdentityMFAStates(r.Context(), payload.TenantID, identityIDs)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load two-step verification settings.")
		return
	}

	results := make([]mfaChangeResult, len(ids))
	var changed []int // indexes into results that need a notice email
	for i, id := range ids {
		results[i] = h.applyUserMFA(r, payload.UserID, id, targets[id], states, enabled)
		if results[i].Status == mfaResultChanged {
			changed = append(changed, i)
		}
	}

	h.sendAdminMFANotices(r, payload.TenantID, ids, targets, results, changed, enabled)

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "enabled": enabled, "results": results})
}

// applyUserMFA makes the change for one member and reports what happened.
func (h *UserOps) applyUserMFA(r *http.Request, actorID, id string, target *userstore.User, states map[string]tenancy.MFAState, enabled bool) mfaChangeResult {
	skip := func(reason string) mfaChangeResult {
		return mfaChangeResult{UserID: id, Status: mfaResultSkipped, Reason: reason}
	}
	if target == nil {
		return skip(mfaSkipNotFound)
	}
	if target.Status == "disabled" {
		return skip(mfaSkipDeactivated)
	}
	// Your own setting goes through Account Settings, which proves the inbox
	// (to turn on) or the password (to turn off).
	if id == actorID {
		return skip(mfaSkipSelf)
	}
	state, found := states[target.IdentityID]
	if !found {
		return skip(mfaSkipNotFound)
	}
	if !state.Available {
		return skip(mfaSkipUnavailable)
	}
	if state.Enabled == enabled {
		return mfaChangeResult{UserID: id, Status: mfaResultUnchanged}
	}
	if err := h.CP.SetIdentityMFAEnabled(r.Context(), target.IdentityID, enabled); err != nil {
		slog.ErrorContext(r.Context(), "admin set mfa", "user", id, "enabled", enabled, "error", err)
		return mfaChangeResult{UserID: id, Status: mfaResultFailed}
	}
	event := "mfa_admin_disabled"
	if enabled {
		event = "mfa_admin_enabled"
	}
	logSecurityEvent(r, event, "identity", target.IdentityID, "user", id, "actor", actorID)
	return mfaChangeResult{UserID: id, Status: mfaResultChanged}
}

// sendAdminMFANotices emails each member whose setting just changed, a few at a
// time. Best effort: the change already happened, so a failed send is reported
// as emailSent=false on that member rather than failing the request.
func (h *UserOps) sendAdminMFANotices(r *http.Request, tenantID string, ids []string, targets map[string]*userstore.User, results []mfaChangeResult, changed []int, enabled bool) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, mfaNoticeConcurrency)
	for _, i := range changed {
		target := targets[ids[i]]
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, target *userstore.User) {
			defer wg.Done()
			defer func() { <-sem }()
			sent := true
			if err := services.SendMFAAdminChangeEmail(r.Context(), tenantID, target.IdentityID, target.Email, target.FullName, enabled); err != nil {
				slog.WarnContext(r.Context(), "mfa admin notice email failed", "identity", target.IdentityID, "enabled", enabled, "error", err)
				sent = false
			}
			results[i].EmailSent = &sent
		}(i, target)
	}
	wg.Wait()
}

// cleanUserIDs validates and de-duplicates the requested ids, keeping order.
// It returns a client-facing message when the list can't be used.
func cleanUserIDs(raw []string) ([]string, string) {
	if len(raw) == 0 {
		return nil, "Select at least one user."
	}
	seen := make(map[string]struct{}, len(raw))
	ids := make([]string, 0, len(raw))
	for _, id := range raw {
		id = strings.ToLower(id)
		if !userIDPattern.MatchString(id) {
			return nil, "One of the user ids is not valid."
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) > maxBulkMFAUsers {
		return nil, "Too many users selected at once."
	}
	return ids, ""
}
