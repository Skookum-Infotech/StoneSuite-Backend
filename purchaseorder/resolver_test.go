package purchaseorder

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
		wantSub string // substring expected in the resolved expression
	}{
		{"system field status", "status", true, "purchase_order_status"},
		{"system field vendor", "vendor_id", true, "purchase_order_vendor_id"},
		{"system field grand total", "grand_total", true, "purchase_order_grand_total"},
		{"custom field", "cf:priority", true, "purchase_order_custom_fields->>'priority'"},
		{"custom field bad key rejected", "cf:1bad;drop", false, ""},
		{"unknown key rejected", "no_such_field", false, ""},
		{"raw column name rejected", "purchase_order_status", false, ""},
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
	if _, _, ok := r.SortExpr("grand_total"); !ok {
		t.Fatal("SortExpr(grand_total) should resolve")
	}
	// expected_date is nullable — must NOT be sortable (breaks keyset cursors).
	if _, _, ok := r.SortExpr("expected_date"); ok {
		t.Fatal("SortExpr(expected_date) must not resolve (nullable column)")
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
