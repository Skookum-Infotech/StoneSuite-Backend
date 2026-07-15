package estimate

import (
	"testing"

	"stonesuite-backend/query"
)

func TestResolver_Resolve(t *testing.T) {
	r := resolver{}

	tests := []struct {
		key     string
		wantOK  bool
		wantDT  query.DataType
	}{
		{"id", true, query.TypeString},
		{"document_number", true, query.TypeString},
		{"customer_id", true, query.TypeString},
		{"status", true, query.TypeString},
		{"grand_total", true, query.TypeNumber},
		{"estimate_date", true, query.TypeDate},
		{"valid_until", true, query.TypeDate},
		{"cf:budget", true, query.TypeString},
		{"cf:Invalid-Key", false, ""},   // fails validCustomKey regex
		{"nonexistent_field", false, ""},
		{"'; DROP TABLE estimate; --", false, ""}, // injection attempt must not resolve
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

func TestResolver_SortExpr(t *testing.T) {
	r := resolver{}
	if _, _, ok := r.SortExpr("grand_total"); !ok {
		t.Error("SortExpr(grand_total) not found, want found")
	}
	if _, _, ok := r.SortExpr("estimate_internal_notes"); ok {
		t.Error("SortExpr(estimate_internal_notes) found, want not found (not in sort whitelist)")
	}
}

func TestResolver_SearchPredicate(t *testing.T) {
	r := resolver{}
	pred := r.SearchPredicate("$1")
	if pred == "" {
		t.Fatal("SearchPredicate returned empty string")
	}
	// Must reference the placeholder, not interpolate a literal value.
	if want := "$1"; !contains(pred, want) {
		t.Errorf("SearchPredicate result %q does not reference placeholder %q", pred, want)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
