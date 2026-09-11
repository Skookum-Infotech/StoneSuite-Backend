package authz

import (
	"context"
	"sort"
)

// noAccessSentinel is embedded as the sole entry when an identity has zero
// read grants (a legitimately locked-down role), instead of returning an
// empty slice. stonesuite-notify treats an EMPTY accessible_resources claim
// as "unrestricted" (its only way to express "no claim yet" for rollout
// safety) — returning a genuinely empty list here would flip a role with no
// read access into seeing every notification, the opposite of intended. A
// sentinel resource no real notification ever carries makes the SQL
// `resource = ANY(...)` filter correctly match nothing.
const noAccessSentinel = "__no_access__"

// AccessibleResources returns the resource strings identityID may Read,
// scoped to activeRoleID exactly like EffectiveGrantsForRole (empty = the
// caller's full assigned role set). For embedding in the JWT
// accessible_resources claim stonesuite-notify filters notifications by.
//
// Returns (nil, nil) — meaning unrestricted — when the identity holds a
// wildcard read grant (ResourceAny/ActionAny, the seeded super_admin role),
// matching stonesuite-notify's own "empty claim = unrestricted" convention.
func AccessibleResources(ctx context.Context, q Querier, identityID, activeRoleID string) ([]string, error) {
	grants, err := EffectiveGrantsForRole(ctx, q, identityID, activeRoleID)
	if err != nil {
		return nil, err
	}
	return accessibleResourcesFromGrants(grants), nil
}

// accessibleResourcesFromGrants is the pure decision logic behind
// AccessibleResources, split out for direct unit testing (mirrors decide()
// in enforcer.go).
func accessibleResourcesFromGrants(grants []Grant) []string {
	seen := map[Resource]bool{}
	for _, g := range grants {
		if g.Action != ActionRead && g.Action != ActionAny {
			continue
		}
		if g.Resource == ResourceAny {
			return nil
		}
		seen[g.Resource] = true
	}
	if len(seen) == 0 {
		return []string{noAccessSentinel}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, string(r))
	}
	sort.Strings(out)
	return out
}
