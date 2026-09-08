package globalsearch

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/userstore"
)

// user has no per-user detail page in the frontend yet -- Domain/Module point at
// the users admin list; the frontend renders these hits without a detail link.
// User administration is inherently "all"-scoped, so the adapter ignores scope.
var _ = addProvider(Provider{Key: "user", Resource: authz.ResourceUser, Domain: "config", Module: "users", Search: searchUsers})

func searchUsers(ctx context.Context, pool *pgxpool.Pool, _ authz.Scope, _, term string, cap int) ([]Result, bool, error) {
	list, hasMore, err := userstore.Search(ctx, pool, term, cap)
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(list))
	for i, u := range list {
		out[i] = Result{Type: "user", ID: u.ID, DisplayName: u.FullName, Subtitle: u.Email, UpdatedAt: u.UpdatedAt}
	}
	return out, hasMore, nil
}
