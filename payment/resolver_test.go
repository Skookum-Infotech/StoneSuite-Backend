package payment

import (
	"strings"
	"testing"

	"stonesuite-backend/query"
)

func TestResolver_Resolve(t *testing.T) {
	r := resolver{}
	expr, dt, ok := r.Resolve("document_number")
	if !ok || dt != query.TypeString || !strings.Contains(expr, "payment_number") {
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
	if !ok || dt != query.TypeNumber || expr != "p.payment_amount" {
		t.Errorf("expected valid sort expression for amount, got %q %v %v", expr, dt, ok)
	}
	if _, _, ok := r.SortExpr("cf:xyz"); ok {
		t.Error("expected custom field to be rejected for sorting")
	}
}

func TestResolver_SearchPredicate(t *testing.T) {
	r := resolver{}
	pred := r.SearchPredicate("$1")
	if !strings.Contains(pred, "p.payment_number ILIKE") || !strings.Contains(pred, "c.customer_name ILIKE") {
		t.Errorf("search predicate missing expected conditions: %s", pred)
	}
}
