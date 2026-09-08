package workflow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/query"
)

func testDefs() []FieldDefinition {
	return []FieldDefinition{
		{Key: "budget", DataType: TypeNumber},
		{Key: "source", DataType: TypeEnum, Options: []string{"web", "ref"}},
	}
}

func TestRecordResolver(t *testing.T) {
	r := newRecordResolver(testDefs())
	cases := []struct {
		key      string
		wantExpr string
		wantDT   query.DataType
		wantOK   bool
	}{
		{"created_at", "created_at", query.TypeDate, true},
		{"status", "current_state_id::text", query.TypeString, true},
		{"cf:budget", "(custom_fields->>'budget')::numeric", query.TypeNumber, true},
		{"cf:source", "custom_fields->>'source'", query.TypeEnum, true},
		{"core:company_name", "core_fields->>'company_name'", query.TypeString, true},
		{"cf:unknown", "", "", false},      // not a defined custom field
		{"core:bad-key", "", "", false},    // fails identifier regex => rejected
		{"core:x'; DROP", "", "", false},   // injection attempt rejected
		{"totally_unknown", "", "", false}, // not system/cf/core
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			expr, dt, ok := r.Resolve(c.key)
			assert.Equal(t, c.wantOK, ok)
			assert.Equal(t, c.wantExpr, expr)
			assert.Equal(t, c.wantDT, dt)
		})
	}
}

func TestRecordResolver_SearchPredicate(t *testing.T) {
	frag := recordResolver{}.SearchPredicate("$3")
	// record number + every conventional core key, all bound to the same $3,
	// OR-ed together inside one parenthesised group.
	assert.True(t, strings.HasPrefix(frag, "(") && strings.HasSuffix(frag, ")"))
	assert.Contains(t, frag, "COALESCE(record_number,'') ILIKE '%'||$3||'%'")
	assert.Contains(t, frag, "core_fields->>'customer_name' ILIKE '%'||$3||'%'")
	assert.Contains(t, frag, "core_fields->>'title' ILIKE '%'||$3||'%'")
	assert.NotContains(t, frag, " AND ") // columns OR within a token
}

func TestBuildRecordQuery_SearchComposesWithScope(t *testing.T) {
	req := query.Request{Search: "acme"}
	sql, args, _, err := buildRecordQuery("wf-1", "own", "user-7", testDefs(), req)
	require.NoError(t, err)
	assert.Contains(t, sql, "owner_user_id = $2")                               // scope still first
	assert.Contains(t, sql, "core_fields->>'customer_name' ILIKE '%'||$3||'%'") // search after scope
	assert.Contains(t, sql, " AND (")                                           // search group ANDed onto scope
	assert.Equal(t, []any{"wf-1", "user-7", "acme"}, args)
}

func TestBuildRecordQuery_ScopeComposition(t *testing.T) {
	req := query.Request{Filters: []query.Clause{{Field: "cf:budget", Op: query.OpGte, Value: float64(100)}}}

	t.Run("own scope narrows by owner and ANDs the filter", func(t *testing.T) {
		sql, args, _, err := buildRecordQuery("wf-1", "own", "user-7", testDefs(), req)
		require.NoError(t, err)
		assert.Contains(t, sql, "workflow_id = $1")
		assert.Contains(t, sql, "owner_user_id = $2")
		// filter param comes AFTER scope params, and is ANDed (never OR with scope)
		assert.Contains(t, sql, "(custom_fields->>'budget')::numeric >= $3")
		assert.NotContains(t, sql, " OR owner_user_id")
		assert.Equal(t, []any{"wf-1", "user-7", float64(100)}, args)
	})

	t.Run("all scope has no owner narrowing", func(t *testing.T) {
		sql, _, _, err := buildRecordQuery("wf-1", "all", "", testDefs(), req)
		require.NoError(t, err)
		assert.NotContains(t, sql, "owner_user_id =") // no owner predicate in WHERE
		assert.Contains(t, sql, ">= $2")              // filter starts right after workflow_id
	})

	// The scope clause is always present and ANDed: the number of " AND "
	// separators proves the filter is appended to, not replacing, scope.
	t.Run("filter never replaces scope", func(t *testing.T) {
		sql, _, _, err := buildRecordQuery("wf-1", "own", "user-7", testDefs(), req)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, strings.Count(sql, " AND "), 2)
	})
}

func TestBuildRecordQuery_InvalidFilterPropagates(t *testing.T) {
	req := query.Request{Filters: []query.Clause{{Field: "cf:nope", Op: query.OpEq, Value: "x"}}}
	_, _, _, err := buildRecordQuery("wf-1", "all", "", testDefs(), req)
	var ife *query.InvalidFilterError
	require.ErrorAs(t, err, &ife)
	assert.Equal(t, "cf:nope", ife.Field)
}

func TestBuildRecordQuery_LimitIsNPlusOne(t *testing.T) {
	req := query.Request{Limit: 25}
	sql, _, _, err := buildRecordQuery("wf-1", "all", "", testDefs(), req)
	require.NoError(t, err)
	assert.Contains(t, sql, "LIMIT 26")
}

func TestBuildRecordCountQuery_ScopeComposition(t *testing.T) {
	filters := []query.Clause{{Field: "cf:budget", Op: query.OpGte, Value: float64(100)}}

	t.Run("own scope narrows by owner and ANDs the filter", func(t *testing.T) {
		sql, args, err := buildRecordCountQuery("wf-1", "own", "user-7", testDefs(), filters)
		require.NoError(t, err)
		assert.Contains(t, sql, "COUNT(*)")
		assert.Contains(t, sql, "workflow_id = $1")
		assert.Contains(t, sql, "owner_user_id = $2")
		assert.Contains(t, sql, "(custom_fields->>'budget')::numeric >= $3")
		assert.NotContains(t, sql, " OR owner_user_id")
		assert.Equal(t, []any{"wf-1", "user-7", float64(100)}, args)
	})

	t.Run("all scope has no owner narrowing", func(t *testing.T) {
		sql, _, err := buildRecordCountQuery("wf-1", "all", "", testDefs(), filters)
		require.NoError(t, err)
		assert.NotContains(t, sql, "owner_user_id =")
		assert.Contains(t, sql, ">= $2")
	})

	t.Run("no filters yields just the scope predicate", func(t *testing.T) {
		sql, args, err := buildRecordCountQuery("wf-1", "own", "user-7", testDefs(), nil)
		require.NoError(t, err)
		assert.NotContains(t, sql, " AND  AND ") // no dangling empty predicate
		assert.Equal(t, []any{"wf-1", "user-7"}, args)
	})

	// Retired/unrecognized scope values must narrow like "own", never like
	// "all" — the same fail-closed contract as every other scope-composed
	// query in this codebase.
	t.Run("unrecognized scope narrows, does not widen", func(t *testing.T) {
		sql, _, err := buildRecordCountQuery("wf-1", "team", "user-7", testDefs(), nil)
		require.NoError(t, err)
		assert.Contains(t, sql, "owner_user_id = $2")
	})
}

func TestBuildRecordCountQuery_InvalidFilterPropagates(t *testing.T) {
	filters := []query.Clause{{Field: "cf:nope", Op: query.OpEq, Value: "x"}}
	_, _, err := buildRecordCountQuery("wf-1", "all", "", testDefs(), filters)
	var ife *query.InvalidFilterError
	require.ErrorAs(t, err, &ife)
	assert.Equal(t, "cf:nope", ife.Field)
}
