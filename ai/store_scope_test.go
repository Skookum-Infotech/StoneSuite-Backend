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
		{"own", "owner_user_id = $2"},
	}
	for _, c := range cases {
		sql, args := buildScopedSearch(c.scope, "user-1", 5)
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
	sql, _ := buildScopedSearch("", "user-1", 5)
	if !strings.Contains(sql, "WHERE FALSE") {
		t.Errorf("empty/unknown scope must fail closed (WHERE FALSE), got:\n%s", sql)
	}

	sql2, _ := buildScopedSearch("bogus-scope", "user-1", 5)
	if !strings.Contains(sql2, "WHERE FALSE") {
		t.Errorf("unrecognized scope must fail closed (WHERE FALSE), got:\n%s", sql2)
	}
}

// TestBuildScopedSearch_OwnScopeOmitsTeamArg confirms "own" binds exactly the
// args it references, keeping $-numbering exact.
func TestBuildScopedSearch_OwnScopeOmitsTeamArg(t *testing.T) {
	sql, args := buildScopedSearch("own", "user-1", 5)
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

// TestBuildScopedLexicalSearch_ScopeNarrowsNeverWidens is the lexical-arm twin
// of TestBuildScopedSearch_ScopeNarrowsNeverWidens: buildScopedLexicalSearch
// MUST use the identical scope clause shapes as the vector arm (both route
// through scopeClause), so a caller can never see more via full-text search
// than via similarity search.
func TestBuildScopedLexicalSearch_ScopeNarrowsNeverWidens(t *testing.T) {
	cases := []struct {
		scope   string
		wantSQL string // substring that MUST be present
	}{
		{"all", "WHERE TRUE"},
		{"own", "owner_user_id = $2"},
	}
	for _, c := range cases {
		sql, args := buildScopedLexicalSearch(c.scope, "user-1", 5)
		if !strings.Contains(sql, c.wantSQL) {
			t.Errorf("scope %s: sql missing %q\n%s", c.scope, c.wantSQL, sql)
		}
		if !strings.Contains(sql, `content_tsv @@ websearch_to_tsquery('simple', $1)`) {
			t.Errorf("scope %s: must full-text match on content_tsv against $1\n%s", c.scope, sql)
		}
		if !strings.Contains(sql, "ORDER BY ts_rank_cd(") {
			t.Errorf("scope %s: must rank by ts_rank_cd\n%s", c.scope, sql)
		}
		// $1 is always reserved for the query text (filled in by
		// SearchScopedLexical); scope params start at $2 and never occupy $1.
		if len(args) == 0 {
			t.Errorf("scope %s: args must reserve a $1 slot for the query text", c.scope)
		} else if args[0] != nil {
			t.Errorf("scope %s: args[0] must stay a nil placeholder from buildScopedLexicalSearch, got %v", c.scope, args[0])
		}
	}
}

// TestBuildScopedLexicalSearch_UnknownScopeFailsClosed mirrors
// TestBuildScopedSearch_UnknownScopeFailsClosed for the lexical arm: an
// unrecognized scope must deny everything, never fall through to "all".
func TestBuildScopedLexicalSearch_UnknownScopeFailsClosed(t *testing.T) {
	sql, _ := buildScopedLexicalSearch("", "user-1", 5)
	if !strings.Contains(sql, "WHERE FALSE") {
		t.Errorf("empty/unknown scope must fail closed (WHERE FALSE), got:\n%s", sql)
	}

	sql2, _ := buildScopedLexicalSearch("bogus-scope", "user-1", 5)
	if !strings.Contains(sql2, "WHERE FALSE") {
		t.Errorf("unrecognized scope must fail closed (WHERE FALSE), got:\n%s", sql2)
	}
}

// TestBuildScopedSearch_RetiredTeamScopeFailsClosed pins the migration
// behaviour after the team scope was retired: a legacy "team" grant still
// sitting in role_permissions must fall through to the fail-closed default and
// deny, never widen a caller's visibility.
func TestBuildScopedSearch_RetiredTeamScopeFailsClosed(t *testing.T) {
	sql, _ := buildScopedSearch("team", "user-1", 5)
	if !strings.Contains(sql, "WHERE FALSE") {
		t.Errorf("retired team scope must fail closed (WHERE FALSE), got:\n%s", sql)
	}
	sql2, _ := buildScopedLexicalSearch("team", "user-1", 5)
	if !strings.Contains(sql2, "WHERE FALSE") {
		t.Errorf("retired team scope must fail closed on lexical arm, got:\n%s", sql2)
	}
}
