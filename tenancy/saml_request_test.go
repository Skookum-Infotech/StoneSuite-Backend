//go:build dbtest

package tenancy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSAMLRequestState(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	t.Run("valid consume", func(t *testing.T) {
		requestID := "req-valid-" + tenantID
		if err := cp.CreateSAMLRequestState(ctx, requestID, tenantID, "cognito", 15*time.Minute); err != nil {
			t.Fatalf("CreateSAMLRequestState: %v", err)
		}
		gotTenantID, err := cp.ConsumeSAMLRequestState(ctx, requestID, "cognito")
		if err != nil {
			t.Fatalf("ConsumeSAMLRequestState: %v", err)
		}
		if gotTenantID != tenantID {
			t.Errorf("tenantID = %q, want %q", gotTenantID, tenantID)
		}
	})

	t.Run("double consume fails", func(t *testing.T) {
		requestID := "req-double-" + tenantID
		if err := cp.CreateSAMLRequestState(ctx, requestID, tenantID, "cognito", 15*time.Minute); err != nil {
			t.Fatalf("CreateSAMLRequestState: %v", err)
		}
		if _, err := cp.ConsumeSAMLRequestState(ctx, requestID, "cognito"); err != nil {
			t.Fatalf("first ConsumeSAMLRequestState: %v", err)
		}
		if _, err := cp.ConsumeSAMLRequestState(ctx, requestID, "cognito"); !errors.Is(err, ErrSAMLRequestNotFound) {
			t.Fatalf("second ConsumeSAMLRequestState err = %v, want ErrSAMLRequestNotFound", err)
		}
	})

	t.Run("expired fails", func(t *testing.T) {
		requestID := "req-expired-" + tenantID
		if err := cp.CreateSAMLRequestState(ctx, requestID, tenantID, "cognito", -time.Minute); err != nil {
			t.Fatalf("CreateSAMLRequestState: %v", err)
		}
		if _, err := cp.ConsumeSAMLRequestState(ctx, requestID, "cognito"); !errors.Is(err, ErrSAMLRequestNotFound) {
			t.Fatalf("err = %v, want ErrSAMLRequestNotFound", err)
		}
	})

	t.Run("wrong provider fails, does not consume", func(t *testing.T) {
		requestID := "req-wrongprovider-" + tenantID
		if err := cp.CreateSAMLRequestState(ctx, requestID, tenantID, "cognito", 15*time.Minute); err != nil {
			t.Fatalf("CreateSAMLRequestState: %v", err)
		}
		if _, err := cp.ConsumeSAMLRequestState(ctx, requestID, "entra"); !errors.Is(err, ErrSAMLRequestNotFound) {
			t.Fatalf("err = %v, want ErrSAMLRequestNotFound", err)
		}
		// A wrong-provider attempt must not consume the row -- the correct
		// provider can still consume it afterward.
		gotTenantID, err := cp.ConsumeSAMLRequestState(ctx, requestID, "cognito")
		if err != nil {
			t.Fatalf("ConsumeSAMLRequestState with correct provider after a wrong-provider attempt: %v", err)
		}
		if gotTenantID != tenantID {
			t.Errorf("tenantID = %q, want %q", gotTenantID, tenantID)
		}
	})

	t.Run("not found - unknown id", func(t *testing.T) {
		if _, err := cp.ConsumeSAMLRequestState(ctx, "no-such-request-id", "cognito"); !errors.Is(err, ErrSAMLRequestNotFound) {
			t.Fatalf("err = %v, want ErrSAMLRequestNotFound", err)
		}
	})
}
