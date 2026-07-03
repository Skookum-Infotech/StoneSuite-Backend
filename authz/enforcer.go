package authz

import "context"

// scopeRank orders scopes from broadest to narrowest for "best wins" resolution.
var scopeRank = map[Scope]int{ScopeOwn: 1, ScopeTeam: 2, ScopeAll: 3}

// ScopeRank exposes scopeRank to callers that need to compare scopes granted
// across multiple resources (e.g. the AI assistant narrowing retrieval to the
// most restrictive scope among several CRM resources it reads). Unknown/empty
// scopes rank 0 — narrower than any real scope.
func ScopeRank(s Scope) int { return scopeRank[s] }

// Decision is the outcome of a permission check.
type Decision struct {
	Allowed bool
	Scope   Scope // broadest scope granted when Allowed; empty otherwise
}

// matches reports whether a grant covers the requested resource/action,
// honoring '*' wildcards (used by the seeded super_admin role).
func (g Grant) matches(r Resource, a Action) bool {
	resOK := g.Resource == r || g.Resource == ResourceAny
	actOK := g.Action == a || g.Action == ActionAny
	return resOK && actOK
}

// decide resolves a set of grants against a requested resource/action,
// returning the broadest matching scope. Pure function — easy to unit test.
func decide(grants []Grant, r Resource, a Action) Decision {
	best := Decision{}
	for _, g := range grants {
		if !g.matches(r, a) {
			continue
		}
		if !best.Allowed || scopeRank[g.Scope] > scopeRank[best.Scope] {
			best = Decision{Allowed: true, Scope: g.Scope}
		}
	}
	return best
}

// Check resolves whether the given identity may perform action a on resource r
// in the tenant backing q, returning the broadest granted scope.
func Check(ctx context.Context, q Querier, identityID string, r Resource, a Action) (Decision, error) {
	grants, err := EffectiveGrants(ctx, q, identityID)
	if err != nil {
		return Decision{}, err
	}
	return decide(grants, r, a), nil
}
