package tenancy

import (
	"strings"
	"testing"

	"stonesuite-backend/models"
)

func TestUnservableReason(t *testing.T) {
	tests := []struct {
		name     string
		tenant   Tenant
		wantCode string
		wantSub  string
	}{
		{"servable tenant has no reason",
			Tenant{Status: StatusActive, MigrationStatus: MigrationOK, DBName: "tenant_acme"}, "", ""},
		{"suspended",
			Tenant{Status: StatusSuspended, MigrationStatus: MigrationOK, DBName: "tenant_acme"},
			models.CodeWorkspaceSuspended, "suspended"},
		{"deleted",
			Tenant{Status: StatusDeleted, MigrationStatus: MigrationOK, DBName: "tenant_acme"},
			models.CodeWorkspaceDeleted, "deleted"},
		{"still provisioning",
			Tenant{Status: StatusProvisioning, MigrationStatus: MigrationPending},
			models.CodeWorkspaceUnavailable, "still being set up"},
		{"application rejected",
			Tenant{Status: StatusRejected},
			models.CodeWorkspaceUnavailable, "not approved"},
		{"awaiting activation",
			Tenant{Status: StatusSubmitted},
			models.CodeWorkspaceUnavailable, "not been activated"},
		{"migration failed",
			Tenant{Status: StatusActive, MigrationStatus: MigrationFailed, DBName: "tenant_acme"},
			models.CodeWorkspaceUnavailable, "maintenance"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := UnservableReason(&tc.tenant)

			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
			if tc.wantCode == "" {
				if msg != "" {
					t.Fatalf("message = %q, want empty for a servable tenant", msg)
				}
				return
			}
			if !strings.Contains(msg, tc.wantSub) {
				t.Fatalf("message %q does not contain %q", msg, tc.wantSub)
			}
		})
	}
}

// A suspended workspace's users are told who can fix it, not just that it is down.
func TestUnservableReason_SuspendedMessageSaysWhoToContact(t *testing.T) {
	_, msg := UnservableReason(&Tenant{Status: StatusSuspended, MigrationStatus: MigrationOK, DBName: "tenant_acme"})

	if !strings.Contains(msg, "contact") {
		t.Fatalf("message %q should tell the user to contact someone", msg)
	}
}
