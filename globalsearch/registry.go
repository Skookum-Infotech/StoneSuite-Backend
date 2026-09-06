// Package globalsearch fans a single search term out to every business
// module the caller has read access to, and merges the results into one
// response grouped by entity type. It is the cross-module counterpart to
// each module's own per-list search box (query.SearchResolver): it does not
// run its own SQL — it calls each module's existing Search/SearchRecords
// function with query.Request{Search: term}, so every module's own RBAC
// scope, filter whitelist, and parameterized ILIKE stay the single source of
// truth for what a caller may see.
//
// registry is the single source of truth for which modules participate in
// global search, mirroring approvalchain's registry pattern. A module missing
// here is silently excluded from global search results (but its own
// dedicated search endpoint is unaffected) — see registry_test.go for the
// guard against a module being forgotten.
package globalsearch

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
)

// Result is the common lightweight shape every provider maps its native
// record into.
type Result struct {
	Type        string    `json:"type"`
	ID          string    `json:"id"`
	Number      string    `json:"number,omitempty"`
	DisplayName string    `json:"displayName"`
	Subtitle    string    `json:"subtitle,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// SearchFunc runs one module's own search with RBAC scope already resolved,
// returning up to cap mapped results plus whether more matched (the module's
// own EffLimit+1/HasMore signal from the query package — no COUNT(*)).
type SearchFunc func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error)

// Provider is one entity type registered for global search.
type Provider struct {
	Key      string         // registry key: JSON group name and &modules= filter value
	Resource authz.Resource // RBAC resource gating this group
	Search   SearchFunc
}

// registry holds every registered provider, keyed by Provider.Key.
var registry = map[string]Provider{}

// addProvider registers p and returns it, so each providers_*.go file can
// register at package-init time via `var _ = addProvider(Provider{...})`.
func addProvider(p Provider) Provider {
	if _, dup := registry[p.Key]; dup {
		panic("globalsearch: duplicate provider key " + p.Key)
	}
	registry[p.Key] = p
	return p
}

// All returns every registered provider.
func All() []Provider {
	out := make([]Provider, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	return out
}
