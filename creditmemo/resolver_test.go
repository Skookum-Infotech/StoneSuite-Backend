package creditmemo

import (
	"strings"
	"testing"

	"stonesuite-backend/query"
)

func TestResolverResolve(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		wantExpr string
		wantType query.DataType
		wantOK   bool
	}{
		{"id", "id", "cm.credit_memo_uuid::text", query.TypeString, true},
		{"status", "status", "cm.credit_memo_status::text", query.TypeString, true},
		{"date", "credit_memo_date", "cm.credit_memo_date", query.TypeDate, true},
		{"grand total", "grand_total", "cm.credit_memo_grand_total", query.TypeNumber, true},
		{"unapplied", "unapplied_amount", "cm.credit_memo_unapplied_amount", query.TypeNumber, true},
		{"customer", "customer_id", "cm.credit_memo_customer_id::text", query.TypeString, true},
		{"custom field", "cf:reason_code", "cm.credit_memo_custom_fields->>'reason_code'", query.TypeString, true},

		// An unresolved key must be reported as unknown so the caller returns
		// 400 — never interpolated into SQL.
		{"unknown key", "nonsense", "", "", false},
		{"sql injection attempt", "id; DROP TABLE credit_memo", "", "", false},
		{"custom field with bad chars", "cf:bad-key", "", "", false},
		{"custom field with quote", "cf:x'; DROP TABLE credit_memo --", "", "", false},
		{"custom field uppercase rejected", "cf:Reason", "", "", false},
		{"custom field empty", "cf:", "", "", false},
		{"empty", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expr, dt, ok := resolver{}.Resolve(tc.key)
			if ok != tc.wantOK || expr != tc.wantExpr || dt != tc.wantType {
				t.Errorf("Resolve(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.key, expr, dt, ok, tc.wantExpr, tc.wantType, tc.wantOK)
			}
		})
	}
}

func TestResolverSortExpr(t *testing.T) {
	// Nullable columns must be filterable but NOT sortable: a NULL breaks
	// keyset-cursor comparison and silently corrupts pagination.
	nullableNotSortable := []string{"invoice_id", "sales_order_id", "owner_id", "sales_rep_id", "currency_id", "price_level_id"}
	for _, key := range nullableNotSortable {
		t.Run("not sortable: "+key, func(t *testing.T) {
			if _, _, ok := (resolver{}).SortExpr(key); ok {
				t.Errorf("SortExpr(%q) is sortable, but the column is nullable — NULLs break keyset pagination", key)
			}
			if _, _, ok := (resolver{}).Resolve(key); !ok {
				t.Errorf("Resolve(%q) not filterable; nullable columns should still be filterable", key)
			}
		})
	}

	sortable := []string{"document_number", "credit_memo_date", "grand_total", "unapplied_amount", "status", "customer_id"}
	for _, key := range sortable {
		t.Run("sortable: "+key, func(t *testing.T) {
			if _, _, ok := (resolver{}).SortExpr(key); !ok {
				t.Errorf("SortExpr(%q) not sortable, want sortable", key)
			}
		})
	}

	if _, _, ok := (resolver{}).SortExpr("nonsense"); ok {
		t.Error("SortExpr(nonsense) resolved, want not ok")
	}
}

// Every sortable field must also be filterable — query.Build resolves the sort
// key through both paths.
func TestSortFieldsAreSubsetOfSystemFields(t *testing.T) {
	for key := range sortFields {
		if _, _, ok := (resolver{}).Resolve(key); !ok {
			t.Errorf("sortable field %q is not in systemFields", key)
		}
	}
}

func TestSearchPredicateIsParameterized(t *testing.T) {
	got := resolver{}.SearchPredicate("$3")
	if !strings.Contains(got, "$3") {
		t.Errorf("SearchPredicate did not use the placeholder: %q", got)
	}
	// The predicate is a trusted hand-written fragment; the only client-supplied
	// value in it must arrive via the placeholder.
	if strings.Count(got, "$") != strings.Count(got, "$3") {
		t.Errorf("SearchPredicate contains a placeholder other than the one passed: %q", got)
	}
	for _, table := range []string{"credit_memo_item", "customer"} {
		if !strings.Contains(got, table) {
			t.Errorf("SearchPredicate missing %s subquery: %q", table, got)
		}
	}
}

func TestResolverImplementsQueryInterfaces(t *testing.T) {
	var _ query.FieldResolver = resolver{}
	var _ query.SortResolver = resolver{}
	var _ query.SearchResolver = resolver{}
}
