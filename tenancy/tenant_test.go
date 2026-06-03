package tenancy

import (
	"context"
	"strings"
	"testing"
)

func TestTenantServable(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		migration string
		dbName    string
		want      bool
	}{
		{"active, migrated, provisioned", StatusActive, MigrationOK, "tenant_acme", true},
		{"active and migrated but no database", StatusActive, MigrationOK, "", false},
		{"active but migration pending", StatusActive, MigrationPending, "tenant_acme", false},
		{"active but migration failed", StatusActive, MigrationFailed, "tenant_acme", false},
		{"provisioning", StatusProvisioning, MigrationOK, "", false},
		{"suspended", StatusSuspended, MigrationOK, "tenant_acme", false},
		{"deleted", StatusDeleted, MigrationOK, "tenant_acme", false},
		{"invited", StatusInvited, MigrationPending, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tn := &Tenant{Status: tc.status, MigrationStatus: tc.migration, DBName: tc.dbName}
			if got := tn.Servable(); got != tc.want {
				t.Fatalf("Servable()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlainDSNResolver(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		wantErr bool
		want    string
	}{
		{"valid ref returns dsn", "postgres://u:p@h:5432/db", false, "postgres://u:p@h:5432/db"},
		{"empty ref errors", "", true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlainDSNResolver(context.Background(), &Tenant{Slug: "acme", DBConnectionRef: tc.ref})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTenantUnservableMessage(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		migration string
		wantSub   string
	}{
		{"provisioning", StatusProvisioning, MigrationOK, "still being set up"},
		{"suspended", StatusSuspended, MigrationOK, "suspended"},
		{"deleted", StatusDeleted, MigrationOK, "deleted"},
		{"migration failed", StatusActive, MigrationFailed, "maintenance"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := tenantUnservableMessage(&Tenant{Status: tc.status, MigrationStatus: tc.migration})
			if msg == "" {
				t.Fatal("expected a non-empty message")
			}
			if !strings.Contains(msg, tc.wantSub) {
				t.Fatalf("message %q does not contain %q", msg, tc.wantSub)
			}
		})
	}
}
