package expense

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
		{"system field status", "status", true, "expense_status"},
		{"system field claimant", "claimant_id", true, "expense_claimant_id"},
		{"system field total", "total", true, "expense_total"},
		{"custom field", "cf:project", true, "expense_custom_fields->>'project'"},
		{"custom field bad key rejected", "cf:1bad;drop", false, ""},
		{"unknown key rejected", "no_such_field", false, ""},
		{"raw column name rejected", "expense_status", false, ""},
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
	if _, _, ok := r.SortExpr("total"); !ok {
		t.Fatal("SortExpr(total) should resolve")
	}
	if _, _, ok := r.SortExpr("claimant_id"); ok {
		t.Fatal("SortExpr(claimant_id) must not resolve (not in sort whitelist)")
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
