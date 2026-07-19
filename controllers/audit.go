package controllers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/auditstore"
	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// AuditOps serves the tenant-wide audit-log browser. Reads are gated on
// audit:read and narrowed by the caller's scope (all/team/own) on the acting
// user, so a team-scoped auditor sees only their team's activity.
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

	// Team scope needs the set of users sharing a team with the caller.
	if decision.Scope == authz.ScopeTeam {
		ids, err := teammateUserIDs(r.Context(), pool, payload.UserID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to resolve team membership.")
			return
		}
		f.CallerTeamUserIDs = ids
	}

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

// teammateUserIDs returns the distinct tenant user ids that share at least one
// team with the caller (including the caller when they belong to a team).
func teammateUserIDs(ctx context.Context, pool *pgxpool.Pool, callerUserID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT tm2.user_id
		FROM team_members tm1
		JOIN team_members tm2 ON tm2.team_id = tm1.team_id
		WHERE tm1.user_id = $1`, callerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
