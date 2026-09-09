//go:build dbtest

package tenancy

import (
	"context"
	"errors"
	"testing"

	"stonesuite-backend/config"
)

// withTestAdminDomain sets config.AppConfig.PlatformAdminEmailDomain for the
// duration of the test, restoring the prior value on cleanup.
func withTestAdminDomain(t *testing.T, domain string) {
	t.Helper()
	orig := config.AppConfig.PlatformAdminEmailDomain
	config.AppConfig.PlatformAdminEmailDomain = domain
	t.Cleanup(func() { config.AppConfig.PlatformAdminEmailDomain = orig })
}

func TestAddPlatformAdmin_RejectsNonMatchingDomain(t *testing.T) {
	cp := newCPTestControlPlane(t)
	withTestAdminDomain(t, "skookuminfotech.com")
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	identity, err := cp.CreateIdentity(ctx, tenantID, "someone-"+tenantID+"@example.com", "", "Someone", true)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	if err := cp.AddPlatformAdmin(ctx, identity.ID); !errors.Is(err, ErrAdminDomainNotAllowed) {
		t.Fatalf("AddPlatformAdmin err = %v, want ErrAdminDomainNotAllowed", err)
	}
	isAdmin, err := cp.IsPlatformAdmin(ctx, identity.ID)
	if err != nil {
		t.Fatalf("IsPlatformAdmin: %v", err)
	}
	if isAdmin {
		t.Error("identity must not have been granted platform admin")
	}
}

func TestAddPlatformAdmin_AllowsMatchingDomain(t *testing.T) {
	cp := newCPTestControlPlane(t)
	withTestAdminDomain(t, "skookuminfotech.com")
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	identity, err := cp.CreateIdentity(ctx, tenantID, "admin-"+tenantID+"@skookuminfotech.com", "", "Admin", true)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	if err := cp.AddPlatformAdmin(ctx, identity.ID); err != nil {
		t.Fatalf("AddPlatformAdmin: %v", err)
	}
	isAdmin, err := cp.IsPlatformAdmin(ctx, identity.ID)
	if err != nil {
		t.Fatalf("IsPlatformAdmin: %v", err)
	}
	if !isAdmin {
		t.Error("expected identity to be a platform admin")
	}
}

// TestIsPlatformAdmin_StaleDomainMismatchIsDenied simulates a platform_admins
// row that predates a domain restriction being configured (or was inserted
// directly against the database, bypassing AddPlatformAdmin): IsPlatformAdmin
// must re-validate the email domain on every read, not just trust the row's
// existence.
func TestIsPlatformAdmin_StaleDomainMismatchIsDenied(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	identity, err := cp.CreateIdentity(ctx, tenantID, "stale-"+tenantID+"@example.com", "", "Stale", true)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	// Bypass AddPlatformAdmin's gate to simulate pre-existing/legacy data.
	if _, err := cp.pool.Exec(ctx, `INSERT INTO platform_admins (identity_id) VALUES ($1)`, identity.ID); err != nil {
		t.Fatalf("seed stale platform_admins row: %v", err)
	}

	withTestAdminDomain(t, "skookuminfotech.com")
	isAdmin, err := cp.IsPlatformAdmin(ctx, identity.ID)
	if err != nil {
		t.Fatalf("IsPlatformAdmin: %v", err)
	}
	if isAdmin {
		t.Error("a stale row for a non-matching-domain identity must not confer platform admin")
	}
}
