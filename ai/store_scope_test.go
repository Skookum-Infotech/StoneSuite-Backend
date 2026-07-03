package ai

import (
	"strings"
	"testing"
)

// TestBuildScopedSearch_ScopeNarrowsNeverWidens is the inviolable test: the
// RBAC scope clause must be ANDed onto the similarity search so a caller can
// only ever see chunks their scope permits — mirroring the Record Filter
// Engine's scope-composition invariant (workflow/filter_test.go). This must
// stay green on every change to buildScopedSearch.
func TestBuildScopedSearch_ScopeNarrowsNeverWidens(t *testing.T) {
	cases := []struct {
		scope   string
		wantSQL string // substring that MUST be present
	}{
		{"all", "WHERE TRUE"},
		{"team", "(owner_user_id = $2 OR team_id = ANY($3))"},
		{"own", "owner_user_id = $2"},
	}
	for _, c := range cases {
		sql, args := buildScopedSearch(c.scope, "user-1", []string{"team-1"}, 5)
		if !strings.Contains(sql, c.wantSQL) {
			t.Errorf("scope %s: sql missing %q\n%s", c.scope, c.wantSQL, sql)
		}
		if !strings.Contains(sql, "embedding <=> $1") {
			t.Errorf("scope %s: must ORDER BY vector distance on $1", c.scope)
		}
		// $1 is always reserved as a placeholder for the query vector (filled
		// in by SearchScoped); scope params start at $2 and never occupy $1.
		if len(args) == 0 {
			t.Errorf("scope %s: args must reserve a $1 slot for the query vector", c.scope)
		} else if args[0] != nil {
			t.Errorf("scope %s: args[0] must stay a nil placeholder from buildScopedSearch, got %v", c.scope, args[0])
		}
	}
}

// TestBuildScopedSearch_UnknownScopeFailsClosed guards the default case: an
// unrecognized scope must deny everything, never fall through to "all".
func TestBuildScopedSearch_UnknownScopeFailsClosed(t *testing.T) {
	sql, _ := buildScopedSearch("", "user-1", nil, 5)
	if !strings.Contains(sql, "WHERE FALSE") {
		t.Errorf("empty/unknown scope must fail closed (WHERE FALSE), got:\n%s", sql)
	}

	sql2, _ := buildScopedSearch("bogus-scope", "user-1", nil, 5)
	if !strings.Contains(sql2, "WHERE FALSE") {
		t.Errorf("unrecognized scope must fail closed (WHERE FALSE), got:\n%s", sql2)
	}
}

// TestBuildScopedSearch_OwnScopeOmitsTeamArg confirms "own" doesn't leak a
// team-scoped arg slot it never binds — keeping $-numbering exact.
func TestBuildScopedSearch_OwnScopeOmitsTeamArg(t *testing.T) {
	sql, args := buildScopedSearch("own", "user-1", []string{"team-1", "team-2"}, 5)
	if strings.Contains(sql, "$3") {
		t.Errorf("own scope must not reference $3, got:\n%s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("own scope args = %d, want 2 ($1 vector, $2 callerUserID)", len(args))
	}
	if args[1] != "user-1" {
		t.Errorf("args[1] = %v, want user-1", args[1])
	}
}
