package chartofaccounts

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

// Filters.clauses must keep placeholder numbering exactly contiguous with
// whatever query.Build already consumed, and must append args in the same
// order as the fragments -- an off-by-one here binds the wrong value to the
// wrong column.
func TestFiltersClauses(t *testing.T) {
	tests := []struct {
		name      string
		filters   Filters
		startIdx  int
		wantFrags []string
		wantArgs  []any
	}{
		{
			name:      "no filters set",
			filters:   Filters{},
			startIdx:  1,
			wantFrags: nil,
			wantArgs:  nil,
		},
		{
			name:      "only Postable set, startIdx 1",
			filters:   Filters{Postable: boolPtr(true)},
			startIdx:  1,
			wantFrags: []string{"a.coa_account_is_postable = $1"},
			wantArgs:  []any{true},
		},
		{
			name:      "only Active set, startIdx 1",
			filters:   Filters{Active: boolPtr(false)},
			startIdx:  1,
			wantFrags: []string{"a.coa_account_is_active = $1"},
			wantArgs:  []any{false},
		},
		{
			name:      "only Visible set, startIdx 1",
			filters:   Filters{Visible: boolPtr(true)},
			startIdx:  1,
			wantFrags: []string{"a.coa_account_is_visible = $1"},
			wantArgs:  []any{true},
		},
		{
			name:      "only SubCategoryID set, startIdx 1",
			filters:   Filters{SubCategoryID: intPtr(1100)},
			startIdx:  1,
			wantFrags: []string{"a.subcategory_id = $1"},
			wantArgs:  []any{1100},
		},
		{
			name:     "all four set, startIdx 1 -- stable order and distinguishable values",
			filters:  Filters{Postable: boolPtr(true), Active: boolPtr(false), Visible: boolPtr(true), SubCategoryID: intPtr(4100)},
			startIdx: 1,
			wantFrags: []string{
				"a.coa_account_is_postable = $1",
				"a.coa_account_is_active = $2",
				"a.coa_account_is_visible = $3",
				"a.subcategory_id = $4",
			},
			wantArgs: []any{true, false, true, 4100},
		},
		{
			name:     "all four set, startIdx 4 -- simulates query.Build having consumed $1-$3",
			filters:  Filters{Postable: boolPtr(true), Active: boolPtr(false), Visible: boolPtr(true), SubCategoryID: intPtr(4100)},
			startIdx: 4,
			wantFrags: []string{
				"a.coa_account_is_postable = $4",
				"a.coa_account_is_active = $5",
				"a.coa_account_is_visible = $6",
				"a.subcategory_id = $7",
			},
			wantArgs: []any{true, false, true, 4100},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frags, args := tt.filters.clauses(tt.startIdx)
			assert.Equal(t, tt.wantFrags, frags)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

// sortValue must return the value the ORDER BY actually sorted on -- returning
// a different field's value silently corrupts keyset pagination (skipped or
// duplicated rows at page boundaries).
func TestSortValue(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	acct := &Account{
		Code:      "1103.01",
		CreatedAt: created,
		UpdatedAt: updated,
	}

	tests := []struct {
		name  string
		field string
		want  any
	}{
		{"code sorts by Code", "code", "1103.01"},
		{"updated_at sorts by UpdatedAt", "updated_at", updated},
		{"created_at sorts by CreatedAt", "created_at", created},
		{"unknown field falls back to CreatedAt (engine default)", "some-unknown-field", created},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sortValue(acct, tt.field))
		})
	}
}

// This closes the latent trap in sortValue's switch: resolver.go's
// sortableFields is the only source of truth for which extra fields are
// sortable, and its exhaustiveness against sortValue is verified only by
// hand. If a future field is added to sortableFields without an explicit
// case in sortValue, it silently falls through to the default (CreatedAt)
// and mints a wrong cursor -- no compile or runtime signal. This test fails
// instead: it asserts every sortableFields key produces a value distinct
// from what an unrecognized field produces, so an unhandled key can't hide
// behind the default branch undetected.
func TestSortValueCoversEverySortableField(t *testing.T) {
	acct := &Account{
		Code:      "9999",
		CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	fallback := sortValue(acct, "__field_not_declared_anywhere__")

	for key := range sortableFields {
		t.Run(key, func(t *testing.T) {
			got := sortValue(acct, key)
			assert.NotEqual(t, fallback, got,
				"sortValue(%q) falls through to the default branch (CreatedAt) -- "+
					"add an explicit case for %q matching resolver.go's sortableFields", key, key)
		})
	}
}
