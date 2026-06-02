package provisioning

import (
	"strings"
	"testing"
)

func TestSanitizeDBName(t *testing.T) {
	tests := []struct {
		name    string
		slug    string
		want    string
		wantErr bool
	}{
		{"simple", "acme", "tenant_acme", false},
		{"uppercase", "Acme", "tenant_acme", false},
		{"spaces and symbols", "Globex Inc.!", "tenant_globex_inc", false},
		{"hyphens", "north-wind", "tenant_north_wind", false},
		{"trailing junk", "acme--", "tenant_acme", false},
		{"empty", "", "", true},
		{"only symbols", "!!!", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SanitizeDBName(tc.slug)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for slug %q", tc.slug)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSQLProviderDSNFor(t *testing.T) {
	p, err := NewSQLProvider("postgres://u:p@host:5432/postgres?sslmode=disable")
	if err != nil {
		t.Fatalf("NewSQLProvider: %v", err)
	}
	dsn, err := p.DSNFor("tenant_acme")
	if err != nil {
		t.Fatalf("DSNFor: %v", err)
	}
	if !strings.Contains(dsn, "/tenant_acme") {
		t.Fatalf("dsn %q does not target tenant_acme", dsn)
	}
	if !strings.Contains(dsn, "sslmode=disable") {
		t.Fatalf("dsn %q lost query params", dsn)
	}
	if _, err := p.DSNFor("Bad Name"); err == nil {
		t.Fatal("expected error for invalid db name")
	}
}
