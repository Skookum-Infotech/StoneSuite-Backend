package vendors

import (
	"strings"
	"testing"

	"stonesuite-backend/query"
)

func TestResolver_Resolve(t *testing.T) {
	r := resolver{}
	expr, dt, ok := r.Resolve("document_number")
	if !ok || dt != query.TypeString || !strings.Contains(expr, "vendor_number") {
		t.Errorf("expected valid string expression for document_number, got %q %v %v", expr, dt, ok)
	}
	expr, dt, ok = r.Resolve("legal_name")
	if !ok || dt != query.TypeString || !strings.Contains(expr, "vendor_legal_name") {
		t.Errorf("expected valid string expression for legal_name, got %q %v %v", expr, dt, ok)
	}
	if _, _, ok = r.Resolve("nope"); ok {
		t.Error("expected unknown field to be rejected")
	}
}

func TestResolver_SortExpr(t *testing.T) {
	r := resolver{}
	expr, dt, ok := r.SortExpr("legal_name")
	if !ok || dt != query.TypeString || expr != "v.vendor_legal_name" {
		t.Errorf("expected valid sort expression for legal_name, got %q %v %v", expr, dt, ok)
	}
	if _, _, ok := r.SortExpr("email"); ok {
		t.Error("expected non-sortable field to be rejected")
	}
}

func TestResolver_SearchPredicate(t *testing.T) {
	r := resolver{}
	pred := r.SearchPredicate("$1")
	if !strings.Contains(pred, "v.vendor_number ILIKE") || !strings.Contains(pred, "v.vendor_legal_name ILIKE") ||
		!strings.Contains(pred, "v.vendor_email ILIKE") {
		t.Errorf("search predicate missing expected conditions: %s", pred)
	}
}
