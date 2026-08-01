package requisition

import (
	"strings"
	"testing"
)

func TestResolverResolve(t *testing.T) {
	r := resolver{}
	tests := []struct {
		name    string
		key     string
		wantOK  bool
		wantSub string
	}{
		{"system field status", "status", true, "requisition_status"},
		{"system field vendor", "vendor_id", true, "requisition_vendor_id"},
		{"system field estimated total", "estimated_total", true, "requisition_estimated_total"},
		{"custom field", "cf:priority", true, "requisition_custom_fields->>'priority'"},
		{"custom field bad key rejected", "cf:1bad;drop", false, ""},
		{"unknown key rejected", "no_such_field", false, ""},
		{"raw column name rejected", "requisition_status", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, _, ok := r.Resolve(tt.key)
			if ok != tt.wantOK {
				t.Fatalf("Resolve(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
			}
			if ok && !strings.Contains(expr, tt.wantSub) {
				t.Fatalf("Resolve(%q) = %q, want substring %q", tt.key, expr, tt.wantSub)
			}
		})
	}
}

func TestResolverSortExpr(t *testing.T) {
	r := resolver{}
	if _, _, ok := r.SortExpr("estimated_total"); !ok {
		t.Fatal("SortExpr(estimated_total) should resolve")
	}
	// needed_by_date is nullable — must NOT be sortable (breaks keyset cursors).
	if _, _, ok := r.SortExpr("needed_by_date"); ok {
		t.Fatal("SortExpr(needed_by_date) must not resolve (nullable column)")
	}
	if _, _, ok := r.SortExpr("cf:anything"); ok {
		t.Fatal("SortExpr must not resolve custom fields")
	}
}

func TestSearchPredicateParameterized(t *testing.T) {
	p := resolver{}.SearchPredicate("$7")
	if !strings.Contains(p, "$7") {
		t.Fatal("SearchPredicate must interpolate the placeholder, not a literal")
	}
	if strings.Contains(p, "%s") || strings.Contains(p, "'%%'") {
		t.Fatal("SearchPredicate must not contain fmt artifacts")
	}
}
