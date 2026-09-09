package controllers

import (
	"testing"

	"stonesuite-backend/config"
	"stonesuite-backend/tenancy"
)

func TestSuperAdminAssignmentAllowed(t *testing.T) {
	orig := config.AppConfig.PlatformAdminEmailDomain
	config.AppConfig.PlatformAdminEmailDomain = "skookuminfotech.com"
	t.Cleanup(func() { config.AppConfig.PlatformAdminEmailDomain = orig })

	tests := []struct {
		name            string
		isPlatformOwner bool
		email           string
		want            bool
	}{
		{"non-platform-owner tenant: any email allowed", false, "anyone@example.com", true},
		{"platform-owner tenant: matching domain allowed", true, "staff@skookuminfotech.com", true},
		{"platform-owner tenant: matching domain, mixed case allowed", true, "Staff@Skookuminfotech.COM", true},
		{"platform-owner tenant: non-matching domain denied", true, "outsider@example.com", false},
		{"platform-owner tenant: look-alike domain denied", true, "attacker@evil-skookuminfotech.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tenant := &tenancy.Tenant{IsPlatformOwner: tt.isPlatformOwner}
			got := superAdminAssignmentAllowed(tenant, tt.email)
			if got != tt.want {
				t.Errorf("superAdminAssignmentAllowed(IsPlatformOwner=%v, %q) = %v, want %v",
					tt.isPlatformOwner, tt.email, got, tt.want)
			}
		})
	}
}
