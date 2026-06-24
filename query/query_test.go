package query

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeResolver maps logical keys to (sqlExpr, dataType) for tests.
type fakeResolver map[string]struct {
	expr string
	dt   DataType
}

func (f fakeResolver) Resolve(key string) (string, DataType, bool) {
	v, ok := f[key]
	return v.expr, v.dt, ok
}

func testResolver() fakeResolver {
	return fakeResolver{
		"id":            {"id", TypeString},
		"created_at":    {"created_at", TypeDate},
		"updated_at":    {"updated_at", TypeDate},
		"record_number": {"record_number", TypeString},
		"company_name":  {"core_fields->>'company_name'", TypeString},
		"status":        {"current_state_id", TypeString},
		"cf:budget":     {"(custom_fields->>'budget')::numeric", TypeNumber},
		"cf:active":     {"(custom_fields->>'active')::boolean", TypeBool},
		"cf:due":        {"(custom_fields->>'due')::timestamptz", TypeDate},
		"cf:source":     {"custom_fields->>'source'", TypeEnum},
	}
}

func TestBuild_FilterSQL(t *testing.T) {
	tests := []struct {
		name      string
		clause    Clause
		wantWhere string
		wantArgs  []any
	}{
		{"eq string", Clause{"company_name", OpEq, "Acme"},
			"core_fields->>'company_name' = $2", []any{"Acme"}},
		{"contains", Clause{"company_name", OpContains, "ac"},
			"core_fields->>'company_name' ILIKE '%' || $2 || '%' ESCAPE '\\'", []any{"ac"}},
		{"startswith", Clause{"company_name", OpStartsWith, "Ac"},
			"core_fields->>'company_name' ILIKE $2 || '%' ESCAPE '\\'", []any{"Ac"}},
		{"number gte", Clause{"cf:budget", OpGte, float64(1000)},
			"(custom_fields->>'budget')::numeric >= $2", []any{float64(1000)}},
		{"number between", Clause{"cf:budget", OpBetween, []any{float64(10), float64(20)}},
			"(custom_fields->>'budget')::numeric BETWEEN $2 AND $3", []any{float64(10), float64(20)}},
		{"in strings", Clause{"cf:source", OpIn, []any{"web", "ref"}},
			"custom_fields->>'source' = ANY($2)", []any{[]string{"web", "ref"}}},
		{"is_null", Clause{"cf:budget", OpIsNull, nil},
			"(custom_fields->>'budget')::numeric IS NULL", nil},
		{"is_empty", Clause{"company_name", OpIsEmpty, nil},
			"(core_fields->>'company_name' IS NULL OR core_fields->>'company_name' = '')", nil},
		{"bool eq", Clause{"cf:active", OpEq, true},
			"(custom_fields->>'active')::boolean = $2", []any{true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := Build(Request{Filters: []Clause{tt.clause}}, testResolver(), 2)
			require.NoError(t, err)
			assert.Equal(t, tt.wantWhere, b.Where)
			assert.Equal(t, tt.wantArgs, b.Args)
		})
	}
}

func TestBuild_ParamOffset(t *testing.T) {
	// startIdx=4 means scope used $1..$3; first filter param must be $4.
	b, err := Build(Request{Filters: []Clause{{"company_name", OpEq, "x"}}}, testResolver(), 4)
	require.NoError(t, err)
	assert.Contains(t, b.Where, "$4")
}

func TestBuild_Errors(t *testing.T) {
	tests := []struct {
		name  string
		req   Request
		field string
	}{
		{"unknown field", Request{Filters: []Clause{{"nope", OpEq, "x"}}}, "nope"},
		{"bad op for type", Request{Filters: []Clause{{"cf:active", OpContains, "x"}}}, "cf:active"},
		{"type mismatch number", Request{Filters: []Clause{{"cf:budget", OpEq, "notnum"}}}, "cf:budget"},
		{"bad date", Request{Filters: []Clause{{"cf:due", OpEq, "13/2020"}}}, "cf:due"},
		{"in not list", Request{Filters: []Clause{{"cf:source", OpIn, "web"}}}, "cf:source"},
		{"between wrong len", Request{Filters: []Clause{{"cf:budget", OpBetween, []any{float64(1)}}}}, "cf:budget"},
		{"too many sorts", Request{Sort: []SortKey{{"created_at", DirAsc}, {"updated_at", DirAsc}}}, "sort"},
		{"unsortable field", Request{Sort: []SortKey{{"company_name", DirAsc}}}, "company_name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(tt.req, testResolver(), 2)
			require.Error(t, err)
			var ife *InvalidFilterError
			require.ErrorAs(t, err, &ife)
			assert.Equal(t, tt.field, ife.Field)
		})
	}
}

func TestBuild_DefaultSortAndLimit(t *testing.T) {
	b, err := Build(Request{}, testResolver(), 2)
	require.NoError(t, err)
	assert.Equal(t, "created_at DESC, id ASC", b.OrderBy)
	assert.Equal(t, DefaultLimit, b.EffLimit)
	assert.Equal(t, defaultSort, b.Sort)
}

func TestBuild_LimitClamp(t *testing.T) {
	assert.Equal(t, DefaultLimit, mustBuild(t, Request{Limit: 0}).EffLimit)
	assert.Equal(t, MaxLimit, mustBuild(t, Request{Limit: 9999}).EffLimit)
	assert.Equal(t, 10, mustBuild(t, Request{Limit: 10}).EffLimit)
}

func TestBuild_DateBoundAsTime(t *testing.T) {
	b, err := Build(Request{Filters: []Clause{{"cf:due", OpGte, "2026-01-02"}}}, testResolver(), 2)
	require.NoError(t, err)
	require.Len(t, b.Args, 1)
	_, ok := b.Args[0].(time.Time)
	assert.True(t, ok, "date value must be bound as time.Time, got %T", b.Args[0])
}

func TestCursor_RoundTripAndKeyset(t *testing.T) {
	created := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	tok := NextCursor("rec-9", defaultSort, created)
	require.NotEmpty(t, tok)

	b, err := Build(Request{Cursor: tok}, testResolver(), 2)
	require.NoError(t, err)
	// desc sort => "<" comparison, plus id tiebreaker.
	assert.Contains(t, b.Keyset, "created_at < $2")
	assert.Contains(t, b.Keyset, "id > $3")
	require.Len(t, b.Args, 2)
	_, ok := b.Args[0].(time.Time)
	assert.True(t, ok)
	assert.Equal(t, "rec-9", b.Args[1])
}

func TestCursor_MalformedIsClientError(t *testing.T) {
	_, err := Build(Request{Cursor: "!!!not-base64!!!"}, testResolver(), 2)
	var ife *InvalidFilterError
	require.ErrorAs(t, err, &ife)
	assert.Equal(t, "cursor", ife.Field)
}

func TestCursor_SortMismatchRejected(t *testing.T) {
	tok := NextCursor("r1", SortKey{"created_at", DirAsc}, time.Now())
	// Request a DESC sort with an ASC-minted cursor.
	_, err := Build(Request{Cursor: tok, Sort: []SortKey{{"created_at", DirDesc}}}, testResolver(), 2)
	var ife *InvalidFilterError
	require.ErrorAs(t, err, &ife)
	assert.Equal(t, "cursor", ife.Field)
}

func TestEscapeLike(t *testing.T) {
	assert.Equal(t, `a\%b\_c\\d`, escapeLike(`a%b_c\d`))
}

func TestBuild_MultipleFiltersAnded(t *testing.T) {
	b := mustBuild(t, Request{Filters: []Clause{
		{"company_name", OpContains, "ac"},
		{"cf:budget", OpGte, float64(100)},
	}})
	assert.Equal(t, 2, strings.Count(b.Where, "ILIKE")+strings.Count(b.Where, ">="))
	assert.Contains(t, b.Where, " AND ")
}

func mustBuild(t *testing.T, req Request) Built {
	t.Helper()
	b, err := Build(req, testResolver(), 2)
	require.NoError(t, err)
	return b
}
