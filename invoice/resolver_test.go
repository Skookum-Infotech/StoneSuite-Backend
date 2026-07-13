package invoice

import (
	"strings"
	"testing"

	"stonesuite-backend/query"
)

func TestResolver_Resolve(t *testing.T) {
	r := resolver{}

	// Valid system field
	expr, dt, ok := r.Resolve("document_number")
	if !ok || dt != query.TypeString || !strings.Contains(expr, "invoice_number") {
		t.Errorf("expected valid string expression for document_number, got %q %v %v", expr, dt, ok)
	}

	// Valid custom field
	expr, dt, ok = r.Resolve("cf:xyz_123")
	if !ok || dt != query.TypeString || !strings.Contains(expr, "xyz_123") {
		t.Errorf("expected valid custom field expression, got %q %v %v", expr, dt, ok)
	}

	// Invalid custom field (injection attempt)
	expr, dt, ok = r.Resolve("cf:xyz'")
	if ok {
		t.Errorf("expected invalid custom field to be rejected, got %q %v", expr, dt)
	}

	// Unknown field
	_, _, ok = r.Resolve("nope")
	if ok {
		t.Error("expected unknown field to be rejected")
	}
}

func TestResolver_SortExpr(t *testing.T) {
	r := resolver{}

	// Valid sort field
	expr, dt, ok := r.SortExpr("grand_total")
	if !ok || dt != query.TypeNumber || expr != "i.invoice_grand_total" {
		t.Errorf("expected valid sort expression for grand_total, got %q %v %v", expr, dt, ok)
	}

	// Invalid sort field
	_, _, ok = r.SortExpr("cf:xyz")
	if ok {
		t.Error("expected custom field to be rejected for sorting")
	}

	// Nullable due_date must NOT be sortable (NULLs break keyset pagination)...
	if _, _, ok := r.SortExpr("due_date"); ok {
		t.Error("due_date is nullable and must be rejected for sorting")
	}
	// ...but it must still be resolvable as a filter field.
	if _, _, ok := r.Resolve("due_date"); !ok {
		t.Error("due_date must remain filterable via Resolve")
	}
}

func TestResolver_SearchPredicate(t *testing.T) {
	r := resolver{}
	pred := r.SearchPredicate("$1")
	if !strings.Contains(pred, "i.invoice_number ILIKE") || !strings.Contains(pred, "c.customer_name ILIKE") {
		t.Errorf("search predicate missing expected conditions: %s", pred)
	}
}
