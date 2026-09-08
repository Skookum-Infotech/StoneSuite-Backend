package userstore

import (
	"context"
	"fmt"
	"strings"

	"stonesuite-backend/query"
)

// Search returns up to limit+1 users whose full name or email contains term
// (case-insensitive), most-recently-updated first, plus whether more matched.
//
// User administration is inherently "all"-scoped -- the users table has no owner
// column and no per-caller narrowing -- so this takes no scope parameter; the
// RBAC permission check on authz.ResourceUser is the only gate. Roles are not
// attached (global search only needs id/name/email).
func Search(ctx context.Context, q Querier, term string, limit int) (users []User, hasMore bool, err error) {
	term = query.StripLikeMeta(strings.TrimSpace(term))
	if term == "" || limit <= 0 {
		return nil, false, nil
	}
	rows, err := q.Query(ctx, `
		SELECT `+userCols+` FROM users u
		WHERE u.full_name ILIKE '%'||$1||'%' OR u.email ILIKE '%'||$1||'%'
		ORDER BY u.updated_at DESC, u.id ASC
		LIMIT $2`, term, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("search users: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.IdentityID, &u.Email, &u.FullName, &u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, false, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("search users: %w", err)
	}

	if len(users) > limit {
		users, hasMore = users[:limit], true
	}
	return users, hasMore, nil
}
