//go:build dbtest

package tenancy

import (
	"context"
	"errors"
	"testing"
	"time"
)

// samlConfigInput returns a valid SAML SSOConfigInput for provider, used to
// seed GetSSOConfigForAuth test scenarios via CreateSSOConfig.
func samlConfigInput(provider string) SSOConfigInput {
	return SSOConfigInput{
		Provider:     provider,
		Protocol:     ssoProtocolSAML,
		MetadataURL:  "https://idp.example.com/metadata",
		IDPEntityID:  "https://idp.example.com/entity",
		SSOURL:       "https://idp.example.com/sso",
		SLOURL:       "https://idp.example.com/slo",
		NameIDFormat: defaultSAMLNameIDFormat,
		Enabled:      true,
	}
}

func TestGetSSOConfigForAuth(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	if _, err := cp.CreateSSOConfig(ctx, tenantID, samlConfigInput("cognito"),
		"", "encrypted-cert-pem", "deadbeef"); err != nil {
		t.Fatalf("seed enabled saml config: %v", err)
	}

	t.Run("found", func(t *testing.T) {
		auth, err := cp.GetSSOConfigForAuth(ctx, tenantID, "cognito")
		if err != nil {
			t.Fatalf("GetSSOConfigForAuth: %v", err)
		}
		if auth.Protocol != ssoProtocolSAML {
			t.Errorf("Protocol = %q, want %q", auth.Protocol, ssoProtocolSAML)
		}
		if auth.CertificatePEMEnc != "encrypted-cert-pem" {
			t.Errorf("CertificatePEMEnc = %q, want encrypted-cert-pem", auth.CertificatePEMEnc)
		}
		if auth.SSOURL != "https://idp.example.com/sso" {
			t.Errorf("SSOURL = %q, want https://idp.example.com/sso", auth.SSOURL)
		}
		if !auth.Enabled {
			t.Error("Enabled = false, want true")
		}
	})

	t.Run("not found - unknown provider", func(t *testing.T) {
		if _, err := cp.GetSSOConfigForAuth(ctx, tenantID, "no-such-provider"); !errors.Is(err, ErrSSOConfigNotFound) {
			t.Fatalf("err = %v, want ErrSSOConfigNotFound", err)
		}
	})

	t.Run("not found - wrong protocol (oidc config, saml lookup)", func(t *testing.T) {
		if _, err := cp.CreateSSOConfig(ctx, tenantID, SSOConfigInput{
			Provider: "entra", ClientID: "client-1", Enabled: true,
		}, "encrypted-secret", "", ""); err != nil {
			t.Fatalf("seed oidc config: %v", err)
		}
		if _, err := cp.GetSSOConfigForAuth(ctx, tenantID, "entra"); !errors.Is(err, ErrSSOConfigNotFound) {
			t.Fatalf("err = %v, want ErrSSOConfigNotFound (an oidc config must not satisfy a saml auth lookup)", err)
		}
	})

	t.Run("not found - disabled", func(t *testing.T) {
		disabled := samlConfigInput("okta")
		disabled.Enabled = false
		if _, err := cp.CreateSSOConfig(ctx, tenantID, disabled, "", "encrypted-cert-pem-2", "cafebabe"); err != nil {
			t.Fatalf("seed disabled saml config: %v", err)
		}
		if _, err := cp.GetSSOConfigForAuth(ctx, tenantID, "okta"); !errors.Is(err, ErrSSOConfigNotFound) {
			t.Fatalf("err = %v, want ErrSSOConfigNotFound (a disabled config must not satisfy an auth lookup)", err)
		}
	})

	t.Run("not found - cross-tenant", func(t *testing.T) {
		otherTenantID := seedTestTenant(t, cp)
		if _, err := cp.GetSSOConfigForAuth(ctx, otherTenantID, "cognito"); !errors.Is(err, ErrSSOConfigNotFound) {
			t.Fatalf("err = %v, want ErrSSOConfigNotFound (cross-tenant provider must not leak)", err)
		}
	})
}

// TestUpdateSSOConfig exercises the full-replace vs. leave-untouched split in
// UpdateSSOConfig's 17-parameter SQL: SSOConfigInput-sourced fields (protocol,
// metadata_url, idp_entity_id, sso_url, slo_url, name_id_format, ...) always
// overwrite; the encrypted-value trio (encSecret/encCertPEM/certFingerprint)
// and metadataFetchedAt only overwrite when passed non-nil.
func TestUpdateSSOConfig(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	created, err := cp.CreateSSOConfig(ctx, tenantID, SSOConfigInput{
		Provider: "entra", ClientID: "client-1", Enabled: true,
	}, "encrypted-secret-v1", "", "")
	if err != nil {
		t.Fatalf("seed oidc config: %v", err)
	}
	if created.Protocol != defaultSSOProtocol {
		t.Fatalf("seed Protocol = %q, want %q", created.Protocol, defaultSSOProtocol)
	}

	// Promote it to a SAML config with a freshly-fetched certificate.
	encCert := "encrypted-cert-v1"
	fingerprint := "fingerprint-v1"
	fetchedAt := time.Now().Truncate(time.Second)
	promoted, err := cp.UpdateSSOConfig(ctx, tenantID, created.ID, SSOConfigInput{
		Provider:     "entra",
		Protocol:     ssoProtocolSAML,
		MetadataURL:  "https://idp.example.com/metadata",
		IDPEntityID:  "https://idp.example.com/entity",
		SSOURL:       "https://idp.example.com/sso",
		SLOURL:       "https://idp.example.com/slo",
		NameIDFormat: defaultSAMLNameIDFormat,
		Enabled:      true,
	}, nil, &encCert, &fingerprint, &fetchedAt)
	if err != nil {
		t.Fatalf("UpdateSSOConfig (promote to saml): %v", err)
	}
	if promoted.Protocol != ssoProtocolSAML {
		t.Errorf("Protocol = %q, want %q", promoted.Protocol, ssoProtocolSAML)
	}
	if promoted.MetadataURL != "https://idp.example.com/metadata" {
		t.Errorf("MetadataURL = %q, want https://idp.example.com/metadata", promoted.MetadataURL)
	}
	if promoted.CertificateFingerprint != fingerprint {
		t.Errorf("CertificateFingerprint = %q, want %q", promoted.CertificateFingerprint, fingerprint)
	}
	if promoted.MetadataFetchedAt == nil || !promoted.MetadataFetchedAt.Equal(fetchedAt) {
		t.Errorf("MetadataFetchedAt = %v, want %v", promoted.MetadataFetchedAt, fetchedAt)
	}
	auth, err := cp.GetSSOConfigForAuth(ctx, tenantID, "entra")
	if err != nil {
		t.Fatalf("GetSSOConfigForAuth after promotion: %v", err)
	}
	if auth.CertificatePEMEnc != encCert {
		t.Errorf("CertificatePEMEnc = %q, want %q", auth.CertificatePEMEnc, encCert)
	}

	// A follow-up update that leaves encSecret/encCertPEM/certFingerprint/
	// metadataFetchedAt nil must not disturb the stored certificate -- only
	// the enabled flag (an SSOConfigInput field) changes.
	untouched, err := cp.UpdateSSOConfig(ctx, tenantID, created.ID, SSOConfigInput{
		Provider:     "entra",
		Protocol:     ssoProtocolSAML,
		MetadataURL:  "https://idp.example.com/metadata",
		IDPEntityID:  "https://idp.example.com/entity",
		SSOURL:       "https://idp.example.com/sso",
		SLOURL:       "https://idp.example.com/slo",
		NameIDFormat: defaultSAMLNameIDFormat,
		Enabled:      false,
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("UpdateSSOConfig (leave cert untouched): %v", err)
	}
	if untouched.Enabled {
		t.Error("Enabled = true, want false after update")
	}
	if untouched.CertificateFingerprint != fingerprint {
		t.Errorf("CertificateFingerprint changed to %q, want unchanged %q", untouched.CertificateFingerprint, fingerprint)
	}
	if untouched.MetadataFetchedAt == nil || !untouched.MetadataFetchedAt.Equal(fetchedAt) {
		t.Errorf("MetadataFetchedAt changed to %v, want unchanged %v", untouched.MetadataFetchedAt, fetchedAt)
	}
	authAfter, err := cp.GetSSOConfigForAuth(ctx, tenantID, "entra")
	if !errors.Is(err, ErrSSOConfigNotFound) {
		t.Fatalf("GetSSOConfigForAuth after disabling: err = %v, auth = %v, want ErrSSOConfigNotFound", err, authAfter)
	}

	if _, err := cp.UpdateSSOConfig(ctx, tenantID, "00000000-0000-0000-0000-000000000000", SSOConfigInput{
		Provider: "entra", Enabled: true,
	}, nil, nil, nil, nil); !errors.Is(err, ErrSSOConfigNotFound) {
		t.Fatalf("UpdateSSOConfig on missing id: err = %v, want ErrSSOConfigNotFound", err)
	}
}
