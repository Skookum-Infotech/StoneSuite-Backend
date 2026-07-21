package controllers

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// recordInScope reports whether a caller granted `scope` may act on a single
// record owned by ownerUserID.
//
// This is the row-level half of RBAC and the guard against IDOR: holding e.g.
// lead:read with scope=own permits reading ONLY your own records, not any
// record id you can guess. The resource/action half is checked separately
// (authz.Check); this narrows that grant to specific rows — exactly as the list
// endpoints already filter by scope, applied here to single-record access too.
//
// The scope model is two-level: `all` sees every row, anything else narrows to
// the caller's own rows (fail-closed — an unknown scope is treated as own).
//
// ownerUserID is the record's tenant users.id. Both store designs populate
// Record.OwnerUserID with the owning users.id (the relational store joins
// employee→users), so the comparison is uniform across designs. Returns false
// (deny) when the caller has no resolvable tenant user, since a caller with no
// profile can own nothing.
func recordInScope(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, ownerUserID string) (bool, error) {
	if scope == authz.ScopeAll {
		return true, nil
	}
	uid, err := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err != nil || uid == "" {
		return false, nil
	}
	return ownerUserID != "" && ownerUserID == uid, nil
}
