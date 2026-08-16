//go:build dbtest

package tenancy

import (
	"context"
	"errors"
	"testing"
)

// TestSSODomainCRUD exercises ListSSODomains/CreateSSODomain/DeleteSSODomain,
// including the tenant-scoping (IDOR) guards documented on each method: a
// cross-tenant sso_config_id on create reads as ErrSSOConfigNotFound, a
// cross-tenant list yields an empty slice rather than another tenant's rows,
// and a cross-tenant delete reads as ErrSSODomainNotFound.
func TestSSODomainCRUD(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	cfg, err := cp.CreateSSOConfig(ctx, tenantID, samlConfigInput("cognito"), "", "encrypted-cert-pem", "deadbeef")
	if err != nil {
		t.Fatalf("seed saml config: %v", err)
	}

	t.Run("create then list", func(t *testing.T) {
		domain := "contoso-" + tenantID + ".com"
		d, err := cp.CreateSSODomain(ctx, tenantID, cfg.ID, domain)
		if err != nil {
			t.Fatalf("CreateSSODomain: %v", err)
		}
		if d.Domain != domain {
			t.Errorf("Domain = %q, want %q", d.Domain, domain)
		}
		if d.SSOConfigID != cfg.ID {
			t.Errorf("SSOConfigID = %q, want %q", d.SSOConfigID, cfg.ID)
		}

		domains, err := cp.ListSSODomains(ctx, tenantID, cfg.ID)
		if err != nil {
			t.Fatalf("ListSSODomains: %v", err)
		}
		found := false
		for _, dd := range domains {
			if dd.ID == d.ID {
				found = true
			}
		}
		if !found {
			t.Error("ListSSODomains did not include the just-created domain")
		}
	})

	t.Run("create - cross-tenant config id reads as not found", func(t *testing.T) {
		otherTenantID := seedTestTenant(t, cp)
		if _, err := cp.CreateSSODomain(ctx, otherTenantID, cfg.ID, "cross-create-"+tenantID+".com"); !errors.Is(err, ErrSSOConfigNotFound) {
			t.Fatalf("err = %v, want ErrSSOConfigNotFound", err)
		}
	})

	t.Run("create - duplicate domain is a unique violation, not a silent success", func(t *testing.T) {
		domain := "dup-" + tenantID + ".com"
		if _, err := cp.CreateSSODomain(ctx, tenantID, cfg.ID, domain); err != nil {
			t.Fatalf("first CreateSSODomain: %v", err)
		}
		if _, err := cp.CreateSSODomain(ctx, tenantID, cfg.ID, domain); err == nil {
			t.Fatal("expected an error registering an already-claimed domain a second time, got nil")
		}
	})

	t.Run("list - cross-tenant config id yields empty, not another tenant's domains", func(t *testing.T) {
		otherTenantID := seedTestTenant(t, cp)
		domains, err := cp.ListSSODomains(ctx, otherTenantID, cfg.ID)
		if err != nil {
			t.Fatalf("ListSSODomains: %v", err)
		}
		if len(domains) != 0 {
			t.Errorf("expected 0 domains for a cross-tenant lookup, got %d", len(domains))
		}
	})

	t.Run("delete then re-delete is not found", func(t *testing.T) {
		domain := "todelete-" + tenantID + ".com"
		d, err := cp.CreateSSODomain(ctx, tenantID, cfg.ID, domain)
		if err != nil {
			t.Fatalf("CreateSSODomain: %v", err)
		}
		if err := cp.DeleteSSODomain(ctx, tenantID, d.ID); err != nil {
			t.Fatalf("DeleteSSODomain: %v", err)
		}
		if err := cp.DeleteSSODomain(ctx, tenantID, d.ID); !errors.Is(err, ErrSSODomainNotFound) {
			t.Fatalf("second DeleteSSODomain err = %v, want ErrSSODomainNotFound", err)
		}
	})

	t.Run("delete - cross-tenant id reads as not found and does not delete", func(t *testing.T) {
		domain := "todelete2-" + tenantID + ".com"
		d, err := cp.CreateSSODomain(ctx, tenantID, cfg.ID, domain)
		if err != nil {
			t.Fatalf("CreateSSODomain: %v", err)
		}
		otherTenantID := seedTestTenant(t, cp)
		if err := cp.DeleteSSODomain(ctx, otherTenantID, d.ID); !errors.Is(err, ErrSSODomainNotFound) {
			t.Fatalf("err = %v, want ErrSSODomainNotFound", err)
		}
		// The cross-tenant delete attempt above must not have removed the row.
		if err := cp.DeleteSSODomain(ctx, tenantID, d.ID); err != nil {
			t.Fatalf("expected the domain to still exist for its real owner: %v", err)
		}
	})
}

// TestDiscoverSSOByEmailDomain mirrors TestGetSSOConfigForAuth's scenarios
// (sso_config_test.go) one layer up: only a domain registered against an
// enabled, protocol=saml config resolves -- an unregistered domain, a domain
// registered against a disabled config, and a domain registered against an
// oidc config must all be indistinguishable "not found" results.
func TestDiscoverSSOByEmailDomain(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	cfg, err := cp.CreateSSOConfig(ctx, tenantID, samlConfigInput("cognito"), "", "encrypted-cert-pem", "deadbeef")
	if err != nil {
		t.Fatalf("seed enabled saml config: %v", err)
	}
	domain := "discover-" + tenantID + ".com"
	if _, err := cp.CreateSSODomain(ctx, tenantID, cfg.ID, domain); err != nil {
		t.Fatalf("seed domain: %v", err)
	}

	t.Run("found", func(t *testing.T) {
		result, err := cp.DiscoverSSOByEmailDomain(ctx, domain)
		if err != nil {
			t.Fatalf("DiscoverSSOByEmailDomain: %v", err)
		}
		if result.TenantID != tenantID {
			t.Errorf("TenantID = %q, want %q", result.TenantID, tenantID)
		}
		if result.Provider != "cognito" {
			t.Errorf("Provider = %q, want cognito", result.Provider)
		}
	})

	t.Run("not found - unregistered domain", func(t *testing.T) {
		if _, err := cp.DiscoverSSOByEmailDomain(ctx, "no-such-domain-"+tenantID+".com"); !errors.Is(err, ErrSSOConfigNotFound) {
			t.Fatalf("err = %v, want ErrSSOConfigNotFound", err)
		}
	})

	t.Run("not found - domain registered against a disabled config", func(t *testing.T) {
		disabled := samlConfigInput("okta")
		disabled.Enabled = false
		disabledCfg, err := cp.CreateSSOConfig(ctx, tenantID, disabled, "", "encrypted-cert-pem-2", "cafebabe")
		if err != nil {
			t.Fatalf("seed disabled saml config: %v", err)
		}
		disabledDomain := "disabled-" + tenantID + ".com"
		if _, err := cp.CreateSSODomain(ctx, tenantID, disabledCfg.ID, disabledDomain); err != nil {
			t.Fatalf("seed domain for disabled config: %v", err)
		}
		if _, err := cp.DiscoverSSOByEmailDomain(ctx, disabledDomain); !errors.Is(err, ErrSSOConfigNotFound) {
			t.Fatalf("err = %v, want ErrSSOConfigNotFound (a disabled config must not satisfy discovery)", err)
		}
	})

	t.Run("not found - domain registered against an oidc config", func(t *testing.T) {
		oidcCfg, err := cp.CreateSSOConfig(ctx, tenantID, SSOConfigInput{
			Provider: "entra", ClientID: "client-1", Enabled: true,
		}, "encrypted-secret", "", "")
		if err != nil {
			t.Fatalf("seed oidc config: %v", err)
		}
		oidcDomain := "oidc-" + tenantID + ".com"
		if _, err := cp.CreateSSODomain(ctx, tenantID, oidcCfg.ID, oidcDomain); err != nil {
			t.Fatalf("seed domain for oidc config: %v", err)
		}
		if _, err := cp.DiscoverSSOByEmailDomain(ctx, oidcDomain); !errors.Is(err, ErrSSOConfigNotFound) {
			t.Fatalf("err = %v, want ErrSSOConfigNotFound (an oidc config must not satisfy saml discovery)", err)
		}
	})
}
