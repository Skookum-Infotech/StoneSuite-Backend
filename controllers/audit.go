package controllers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"stonesuite-backend/auditstore"
	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// AuditOps serves the tenant-wide audit-log browser. Reads are gated on
// audit:read and narrowed by the caller's scope on the acting user: `all` sees
// every entry, anything else sees only the caller's own.
type AuditOps struct{}

// NewAuditOps constructs the audit handler group.
func NewAuditOps() *AuditOps { return &AuditOps{} }

// ListAudit GET /api/tenant/audit
//
// Query params (all optional): resource, action, actor (user id), from, to
// (RFC3339), limit (1..100), cursor (opaque keyset from a previous page).
func (h *AuditOps) ListAudit(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceAudit, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceAudit), "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read the audit log.")
		return
	}

	f := auditstore.Filter{
		Resource:     r.URL.Query().Get("resource"),
		Action:       r.URL.Query().Get("action"),
		Actor:        r.URL.Query().Get("actor"),
		Cursor:       r.URL.Query().Get("cursor"),
		Scope:        string(decision.Scope),
		CallerUserID: payload.UserID,
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			fail(w, http.StatusBadRequest, "limit must be a non-negative integer.")
			return
		}
		f.Limit = n
	}
	from, ok := parseAuditTime(w, r.URL.Query().Get("from"), "from")
	if !ok {
		return
	}
	f.From = from
	to, ok := parseAuditTime(w, r.URL.Query().Get("to"), "to")
	if !ok {
		return
	}
	f.To = to

	entries, next, err := auditstore.List(r.Context(), pool, f)
	if errors.Is(err, auditstore.ErrInvalidCursor) {
		fail(w, http.StatusBadRequest, "Invalid pagination cursor.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list audit log.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"entries":     entries,
		"next_cursor": next,
	})
}

// parseAuditTime parses an optional RFC3339 timestamp query param. A blank value
// yields (nil, true); a malformed value writes 400 and returns ok=false.
func parseAuditTime(w http.ResponseWriter, v, field string) (*time.Time, bool) {
	if v == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		fail(w, http.StatusBadRequest, field+" must be an RFC3339 timestamp.")
		return nil, false
	}
	return &t, true
}
