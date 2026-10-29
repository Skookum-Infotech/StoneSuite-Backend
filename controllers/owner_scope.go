package controllers

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// ownerEmployeeFilter resolves the owner-employee filter a list/search must
// apply for the caller's scope: nil for scope=all, the caller's employee id
// otherwise. ok=false means the caller is own-scoped but has no employee row,
// so they own nothing and the list must be empty (fail closed).
func ownerEmployeeFilter(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (owner *int, ok bool) {
	if scope == authz.ScopeAll {
		return nil, true
	}
	id, found := workflow.EmployeeIDByIdentity(ctx, pool, identityID)
	if !found {
		return nil, false
	}
	return &id, true
}

// ownerEmployeeAllows reports whether a caller with `scope` may act on an
// inventory document whose owner employee id is ownerID (the IDOR guard for the
// inventory documents and customers, which store the owner as an
// employee id rather than a users.id).
func ownerEmployeeAllows(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string, ownerID *int) bool {
	if scope == authz.ScopeAll {
		return true
	}
	id, found := workflow.EmployeeIDByIdentity(ctx, pool, identityID)
	return found && ownerID != nil && *ownerID == id
}
