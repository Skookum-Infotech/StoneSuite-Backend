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
		{"cf:unknown", "", "", false},          // not a defined custom field
		{"core:bad-key", "", "", false},        // fails identifier regex => rejected
		{"core:x'; DROP", "", "", false},       // injection attempt rejected
		{"totally_unknown", "", "", false},     // not system/cf/core
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

func TestBuildRecordQuery_ScopeComposition(t *testing.T) {
	req := query.Request{Filters: []query.Clause{{Field: "cf:budget", Op: query.OpGte, Value: float64(100)}}}

	t.Run("own scope narrows by owner and ANDs the filter", func(t *testing.T) {
		sql, args, _, err := buildRecordQuery("wf-1", "own", "user-7", nil, testDefs(), req)
		require.NoError(t, err)
		assert.Contains(t, sql, "workflow_id = $1")
		assert.Contains(t, sql, "owner_user_id = $2")
		// filter param comes AFTER scope params, and is ANDed (never OR with scope)
		assert.Contains(t, sql, "(custom_fields->>'budget')::numeric >= $3")
		assert.NotContains(t, sql, " OR owner_user_id")
		assert.Equal(t, []any{"wf-1", "user-7", float64(100)}, args)
	})

	t.Run("team scope adds team membership and offsets filter params", func(t *testing.T) {
		sql, args, _, err := buildRecordQuery("wf-1", "team", "user-7", []string{"team-a"}, testDefs(), req)
		require.NoError(t, err)
		assert.Contains(t, sql, "(owner_user_id = $2 OR team_id = ANY($3))")
		assert.Contains(t, sql, ">= $4") // filter starts at $4 after 3 scope params
		assert.Equal(t, []any{"wf-1", "user-7", []string{"team-a"}, float64(100)}, args)
	})

	t.Run("all scope has no owner narrowing", func(t *testing.T) {
		sql, _, _, err := buildRecordQuery("wf-1", "all", "", nil, testDefs(), req)
		require.NoError(t, err)
		assert.NotContains(t, sql, "owner_user_id =") // no owner predicate in WHERE
		assert.Contains(t, sql, ">= $2")              // filter starts right after workflow_id
	})

	// The scope clause is always present and ANDed: the number of " AND "
	// separators proves the filter is appended to, not replacing, scope.
	t.Run("filter never replaces scope", func(t *testing.T) {
		sql, _, _, err := buildRecordQuery("wf-1", "own", "user-7", nil, testDefs(), req)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, strings.Count(sql, " AND "), 2)
	})
}

func TestBuildRecordQuery_InvalidFilterPropagates(t *testing.T) {
	req := query.Request{Filters: []query.Clause{{Field: "cf:nope", Op: query.OpEq, Value: "x"}}}
	_, _, _, err := buildRecordQuery("wf-1", "all", "", nil, testDefs(), req)
	var ife *query.InvalidFilterError
	require.ErrorAs(t, err, &ife)
	assert.Equal(t, "cf:nope", ife.Field)
}

func TestBuildRecordQuery_LimitIsNPlusOne(t *testing.T) {
	req := query.Request{Limit: 25}
	sql, _, _, err := buildRecordQuery("wf-1", "all", "", nil, testDefs(), req)
	require.NoError(t, err)
	assert.Contains(t, sql, "LIMIT 26")
}
