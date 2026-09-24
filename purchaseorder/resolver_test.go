package purchaseorder

import (
	"errors"
	"strings"
	"testing"

	"stonesuite-backend/query"
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
		{"system field vendor uuid", "vendor_uuid", true, "v.vendor_uuid::text"},
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

// vendor_uuid pins an order to a vendor by the public id the API returns as
// `vendor.id`. The value must only ever be bound, never interpolated, and the
// expression must stay parenthesized so every operator composes with it.
func TestVendorUUIDFilter(t *testing.T) {
	const wantExpr = "(SELECT v.vendor_uuid::text FROM vendor v WHERE v.vendor_id = po.purchase_order_vendor_id)"

	tests := []struct {
		name      string
		clause    query.Clause
		wantWhere string
		wantArgs  int
	}{
		{"eq binds the uuid", query.Clause{Field: "vendor_uuid", Op: query.OpEq, Value: "3f2b8c1e-9a4d-4e6f-8b7a-1c2d3e4f5a6b"}, wantExpr + " = $1", 1},
		{"in binds one list", query.Clause{Field: "vendor_uuid", Op: query.OpIn, Value: []any{"a", "b"}}, wantExpr + " = ANY($1)", 1},
		{"is_null composes with the parenthesized expr", query.Clause{Field: "vendor_uuid", Op: query.OpIsNull}, wantExpr + " IS NULL", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			built, err := query.Build(query.Request{Filters: []query.Clause{tt.clause}}, resolver{}, 1)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !strings.Contains(built.Where, tt.wantWhere) {
				t.Errorf("Where = %q, want it to contain %q", built.Where, tt.wantWhere)
			}
			if len(built.Args) != tt.wantArgs {
				t.Errorf("Args = %v, want %d bound value(s)", built.Args, tt.wantArgs)
			}
		})
	}

	t.Run("a hostile value is bound, never interpolated", func(t *testing.T) {
		const hostile = "x' OR 1=1; DROP TABLE vendor; --"
		built, err := query.Build(query.Request{Filters: []query.Clause{
			{Field: "vendor_uuid", Op: query.OpEq, Value: hostile},
		}}, resolver{}, 1)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if strings.Contains(built.Where, "DROP") || strings.Contains(built.Where, "1=1") {
			t.Errorf("Where = %q, the value leaked into the SQL text", built.Where)
		}
		if len(built.Args) != 1 || built.Args[0] != hostile {
			t.Errorf("Args = %v, want the value bound verbatim as $1", built.Args)
		}
	})

	t.Run("placeholders continue from the caller's index", func(t *testing.T) {
		// Search prepends a scope clause using $1 and starts the builder at 2.
		built, err := query.Build(query.Request{Filters: []query.Clause{
			{Field: "vendor_uuid", Op: query.OpEq, Value: "a"},
		}}, resolver{}, 2)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if !strings.Contains(built.Where, wantExpr+" = $2") {
			t.Errorf("Where = %q, want the vendor filter bound at $2", built.Where)
		}
	})

	t.Run("is filterable but not sortable", func(t *testing.T) {
		if _, _, ok := (resolver{}).SortExpr("vendor_uuid"); ok {
			t.Fatal("SortExpr(vendor_uuid) must not resolve: a text uuid is no stable keyset order")
		}
		_, err := query.Build(query.Request{Sort: []query.SortKey{{Field: "vendor_uuid", Dir: query.DirAsc}}}, resolver{}, 1)
		var ife *query.InvalidFilterError
		if !errors.As(err, &ife) {
			t.Fatalf("Build with a vendor_uuid sort err = %v, want an *InvalidFilterError (400)", err)
		}
	})

	t.Run("a misspelled key is a 400, never SQL", func(t *testing.T) {
		_, err := query.Build(query.Request{Filters: []query.Clause{
			{Field: "vendor_uid", Op: query.OpEq, Value: "a"},
		}}, resolver{}, 1)
		var ife *query.InvalidFilterError
		if !errors.As(err, &ife) {
			t.Fatalf("Build with an unknown key err = %v, want an *InvalidFilterError (400)", err)
		}
	})
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
