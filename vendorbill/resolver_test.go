package vendorbill

import (
	"strings"
	"testing"

	"stonesuite-backend/query"
)

func TestResolverResolve(t *testing.T) {
	r := resolver{}
	tests := []struct {
		key    string
		wantOK bool
		wantDT query.DataType
	}{
		{"id", true, query.TypeString},
		{"grand_total", true, query.TypeNumber},
		{"balance_due", true, query.TypeNumber},
		{"bill_date", true, query.TypeDate},
		{"cf:priority", true, query.TypeString},
		{"cf:INVALID KEY", false, ""},
		{"not_a_real_field", false, ""},
	}
	for _, tt := range tests {
		_, dt, ok := r.Resolve(tt.key)
		if ok != tt.wantOK {
			t.Errorf("Resolve(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
			continue
		}
		if ok && dt != tt.wantDT {
			t.Errorf("Resolve(%q) dt = %v, want %v", tt.key, dt, tt.wantDT)
		}
	}
}

func TestResolverSortExpr(t *testing.T) {
	r := resolver{}
	if _, _, ok := r.SortExpr("grand_total"); !ok {
		t.Error("SortExpr(grand_total) should be sortable")
	}
	if _, _, ok := r.SortExpr("due_date"); ok {
		t.Error("SortExpr(due_date) should NOT be sortable -- nullable column breaks keyset pagination")
	}
}

func TestResolverSearchPredicate(t *testing.T) {
	r := resolver{}
	frag := r.SearchPredicate("$3")
	if !strings.Contains(frag, "$3") {
		t.Errorf("SearchPredicate must reference the given placeholder, got: %s", frag)
	}
	if !strings.Contains(frag, "vendor_bill_vendor_invoice_number") {
		t.Error("SearchPredicate must search the vendor's own invoice number")
	}
}
