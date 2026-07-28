package cashtransfer

import "testing"

func TestResolverResolve(t *testing.T) {
	r := resolver{}
	tests := []struct {
		key    string
		wantOK bool
	}{
		{"id", true},
		{"document_number", true},
		{"record_number", true},
		{"status", true},
		{"from_account_id", true},
		{"to_account_id", true},
		{"amount", true},
		{"transfer_date", true},
		{"reference", true},
		{"owner_id", true},
		{"created_at", true},
		{"updated_at", true},
		{"cf:department", true},
		{"cf:INVALID KEY", false},
		{"not_a_real_field", false},
	}
	for _, tt := range tests {
		if _, _, ok := r.Resolve(tt.key); ok != tt.wantOK {
			t.Errorf("Resolve(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
		}
	}
}

func TestResolverSortExpr(t *testing.T) {
	r := resolver{}
	tests := []struct {
		key    string
		wantOK bool
	}{
		{"document_number", true},
		{"record_number", true},
		{"transfer_date", true},
		{"amount", true},
		{"status", true},
		{"not_sortable", false},
	}
	for _, tt := range tests {
		if _, _, ok := r.SortExpr(tt.key); ok != tt.wantOK {
			t.Errorf("SortExpr(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
		}
	}
}
