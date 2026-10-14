package bintransfer

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
		{"status", true, query.TypeString},
		// The code (DRFT, PAPV…), which is what a list's status chips filter on.
		{"status_code", true, query.TypeString},
		{"warehouse_id", true, query.TypeString},
		{"transfer_date", true, query.TypeDate},
		{"notes", true, query.TypeString},
		{"owner_id", true, query.TypeString},
		{"created_at", true, query.TypeDate},
		{"updated_at", true, query.TypeDate},
		{"completed_at", true, query.TypeDate},
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
	for _, key := range []string{"document_number", "record_number", "transfer_date", "status", "created_at", "updated_at"} {
		if _, _, ok := r.SortExpr(key); !ok {
			t.Errorf("SortExpr(%q) should be sortable", key)
		}
	}
	if _, _, ok := r.SortExpr("not_a_real_field"); ok {
		t.Error("SortExpr(not_a_real_field) should NOT be sortable")
	}
}

// Searching a slab serial has to find the transfer it sits on -- that is the
// first thing a yard lead asks: "which transfer is this slab in?".
func TestResolverSearchPredicate(t *testing.T) {
	frag := resolver{}.SearchPredicate("$3")
	for _, want := range []string{"$3", "bin_transfer_number", "bin_transfer_notes", "slab_serial"} {
		if !strings.Contains(frag, want) {
			t.Errorf("SearchPredicate must reference %q, got: %s", want, frag)
		}
	}
}
