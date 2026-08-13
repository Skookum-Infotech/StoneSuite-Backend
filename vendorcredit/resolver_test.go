package vendorcredit

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
		{"document_number", true, query.TypeString},
		{"record_number", true, query.TypeString},
		{"vendor_id", true, query.TypeString},
		{"status", true, query.TypeString},
		{"reference_number", true, query.TypeString},
		{"credit_date", true, query.TypeDate},
		{"reason", true, query.TypeString},
		{"grand_total", true, query.TypeNumber},
		{"applied_total", true, query.TypeNumber},
		{"unapplied_amount", true, query.TypeNumber},
		{"owner_id", true, query.TypeString},
		{"created_at", true, query.TypeDate},
		{"updated_at", true, query.TypeDate},
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
	sortable := []string{
		"document_number",
		"record_number",
		"credit_date",
		"grand_total",
		"unapplied_amount",
		"status",
		"vendor_id",
	}
	for _, key := range sortable {
		if _, _, ok := r.SortExpr(key); !ok {
			t.Errorf("SortExpr(%q) should be sortable", key)
		}
	}
	if _, _, ok := r.SortExpr("not_a_real_field"); ok {
		t.Error("SortExpr(not_a_real_field) should NOT be sortable")
	}
}

func TestResolverSearchPredicate(t *testing.T) {
	r := resolver{}
	frag := r.SearchPredicate("$3")
	if !strings.Contains(frag, "$3") {
		t.Errorf("SearchPredicate must reference the given placeholder, got: %s", frag)
	}
	if !strings.Contains(frag, "vendor_credit_number") {
		t.Error("SearchPredicate must search the vendor credit's own document number")
	}
	if !strings.Contains(frag, "vendor_credit_reference_number") {
		t.Error("SearchPredicate must search the reference number")
	}
	if !strings.Contains(frag, "vendor_legal_name") {
		t.Error("SearchPredicate must search the vendor's legal name")
	}
}
