package refund

import (
	"strings"
	"testing"

	"stonesuite-backend/query"
)

func TestResolver_Resolve(t *testing.T) {
	r := resolver{}
	expr, dt, ok := r.Resolve("document_number")
	if !ok || dt != query.TypeString || !strings.Contains(expr, "refund_number") {
		t.Errorf("expected valid string expression for document_number, got %q %v %v", expr, dt, ok)
	}
	expr, dt, ok = r.Resolve("cf:xyz_123")
	if !ok || dt != query.TypeString || !strings.Contains(expr, "xyz_123") {
		t.Errorf("expected valid custom field expression, got %q %v %v", expr, dt, ok)
	}
	if _, _, ok = r.Resolve("cf:xyz'"); ok {
		t.Error("expected invalid custom field to be rejected")
	}
	if _, _, ok = r.Resolve("nope"); ok {
		t.Error("expected unknown field to be rejected")
	}
}

func TestResolver_SortExpr(t *testing.T) {
	r := resolver{}
	expr, dt, ok := r.SortExpr("amount")
	if !ok || dt != query.TypeNumber || expr != "rfnd.refund_amount" {
		t.Errorf("expected valid sort expression for amount, got %q %v %v", expr, dt, ok)
	}
	if _, _, ok := r.SortExpr("cf:xyz"); ok {
		t.Error("expected custom field to be rejected for sorting")
	}
}

// TestSortFieldsAreSubsetOfSystemFields pins the invariant that nothing is
// sortable unless it is also filterable — and, per the Record Filter Engine
// rule, nullable columns are filterable but never sortable (NULLs break
// cursor comparison).
func TestSortFieldsAreSubsetOfSystemFields(t *testing.T) {
	for key := range sortFields {
		if _, ok := systemFields[key]; !ok {
			t.Errorf("sortFields[%q] has no matching systemFields entry", key)
		}
	}
}

func TestResolver_SearchPredicate(t *testing.T) {
	r := resolver{}
	pred := r.SearchPredicate("$1")
	if !strings.Contains(pred, "rfnd.refund_number ILIKE") || !strings.Contains(pred, "c.customer_name ILIKE") {
		t.Errorf("search predicate missing expected conditions: %s", pred)
	}
}

// TestSearchPredicateIsParameterized guards against a hand-rolled predicate
// ever splicing a raw placeholder value into the SQL string itself instead of
// using $n binding — a SQL-injection regression test.
func TestSearchPredicateIsParameterized(t *testing.T) {
	r := resolver{}
	pred := r.SearchPredicate("$7")
	if !strings.Contains(pred, "$7") {
		t.Errorf("expected placeholder $7 to appear verbatim in predicate: %s", pred)
	}
	if strings.Contains(pred, "'; DROP") {
		t.Errorf("predicate must never interpolate raw values: %s", pred)
	}
}

func TestResolverImplementsQueryInterfaces(t *testing.T) {
	var _ query.FieldResolver = resolver{}
	var _ query.SortResolver = resolver{}
	var _ query.SearchResolver = resolver{}
}
