//go:build dbtest

package tenancy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSAMLLoginCode(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)
	identity, err := cp.CreateIdentity(ctx, tenantID, "login-code-"+tenantID+"@example.com", "", "Login Code User", true)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	t.Run("valid consume", func(t *testing.T) {
		code := "code-valid-" + tenantID
		if err := cp.CreateSAMLLoginCode(ctx, code, identity.ID, tenantID, time.Minute); err != nil {
			t.Fatalf("CreateSAMLLoginCode: %v", err)
		}
		gotIdentityID, gotTenantID, err := cp.ConsumeSAMLLoginCode(ctx, code)
		if err != nil {
			t.Fatalf("ConsumeSAMLLoginCode: %v", err)
		}
		if gotIdentityID != identity.ID {
			t.Errorf("identityID = %q, want %q", gotIdentityID, identity.ID)
		}
		if gotTenantID != tenantID {
			t.Errorf("tenantID = %q, want %q", gotTenantID, tenantID)
		}
	})

	t.Run("double consume fails", func(t *testing.T) {
		code := "code-double-" + tenantID
		if err := cp.CreateSAMLLoginCode(ctx, code, identity.ID, tenantID, time.Minute); err != nil {
			t.Fatalf("CreateSAMLLoginCode: %v", err)
		}
		if _, _, err := cp.ConsumeSAMLLoginCode(ctx, code); err != nil {
			t.Fatalf("first ConsumeSAMLLoginCode: %v", err)
		}
		if _, _, err := cp.ConsumeSAMLLoginCode(ctx, code); !errors.Is(err, ErrSAMLLoginCodeNotFound) {
			t.Fatalf("second ConsumeSAMLLoginCode err = %v, want ErrSAMLLoginCodeNotFound", err)
		}
	})

	t.Run("expired fails", func(t *testing.T) {
		code := "code-expired-" + tenantID
		if err := cp.CreateSAMLLoginCode(ctx, code, identity.ID, tenantID, -time.Minute); err != nil {
			t.Fatalf("CreateSAMLLoginCode: %v", err)
		}
		if _, _, err := cp.ConsumeSAMLLoginCode(ctx, code); !errors.Is(err, ErrSAMLLoginCodeNotFound) {
			t.Fatalf("err = %v, want ErrSAMLLoginCodeNotFound", err)
		}
	})

	t.Run("not found - unknown code", func(t *testing.T) {
		if _, _, err := cp.ConsumeSAMLLoginCode(ctx, "no-such-code"); !errors.Is(err, ErrSAMLLoginCodeNotFound) {
			t.Fatalf("err = %v, want ErrSAMLLoginCodeNotFound", err)
		}
	})
}
