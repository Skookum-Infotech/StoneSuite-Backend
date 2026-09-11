//go:build dbtest

package tenancy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPortalInviteForCustomer(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	mkAcceptedInvite := func(t *testing.T, customerUUID string) *PortalInvite {
		t.Helper()
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		email := "customer-" + suffix + "@example.com"
		identity, err := cp.CreateIdentity(ctx, tenantID, email, "hash", "Test Customer", true)
		if err != nil {
			t.Fatalf("CreateIdentity: %v", err)
		}
		inv, err := cp.CreatePortalInvite(ctx, tenantID, identity.ID, email, "Test Customer",
			customerUUID, "tok-"+suffix, "", time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatalf("CreatePortalInvite: %v", err)
		}
		if _, err := cp.CreatePortalLink(ctx, identity.ID, tenantID); err != nil {
			t.Fatalf("CreatePortalLink: %v", err)
		}
		if err := cp.MarkPortalInviteAccepted(ctx, inv.ID); err != nil {
			t.Fatalf("MarkPortalInviteAccepted: %v", err)
		}
		got, err := cp.PortalInviteByToken(ctx, inv.Token)
		if err != nil {
			t.Fatalf("PortalInviteByToken (re-fetch accepted): %v", err)
		}
		return got
	}

	t.Run("no invite for this customer returns ErrPortalInviteNotFound", func(t *testing.T) {
		_, err := cp.PortalInviteForCustomer(ctx, tenantID, "00000000-0000-0000-0000-000000000001")
		if !errors.Is(err, ErrPortalInviteNotFound) {
			t.Fatalf("err = %v, want ErrPortalInviteNotFound", err)
		}
	})

	t.Run("pending (not yet accepted) invite is not returned", func(t *testing.T) {
		customerUUID := "00000000-0000-0000-0000-000000000002"
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		email := "pending-" + suffix + "@example.com"
		identity, err := cp.CreateIdentity(ctx, tenantID, email, "hash", "Pending Customer", true)
		if err != nil {
			t.Fatalf("CreateIdentity: %v", err)
		}
		if _, err := cp.CreatePortalInvite(ctx, tenantID, identity.ID, email, "Pending Customer",
			customerUUID, "tok-"+suffix, "", time.Now().Add(48*time.Hour)); err != nil {
			t.Fatalf("CreatePortalInvite: %v", err)
		}

		_, err = cp.PortalInviteForCustomer(ctx, tenantID, customerUUID)
		if !errors.Is(err, ErrPortalInviteNotFound) {
			t.Fatalf("err = %v, want ErrPortalInviteNotFound for a still-pending invite", err)
		}
	})

	t.Run("accepted invite is found and matches the identity", func(t *testing.T) {
		customerUUID := "00000000-0000-0000-0000-000000000003"
		accepted := mkAcceptedInvite(t, customerUUID)

		got, err := cp.PortalInviteForCustomer(ctx, tenantID, customerUUID)
		if err != nil {
			t.Fatalf("PortalInviteForCustomer: %v", err)
		}
		if got.IdentityID != accepted.IdentityID {
			t.Fatalf("IdentityID = %q, want %q", got.IdentityID, accepted.IdentityID)
		}
		if got.CustomerUUID != customerUUID {
			t.Fatalf("CustomerUUID = %q, want %q", got.CustomerUUID, customerUUID)
		}
		if got.Status != PortalInviteStatusAccepted {
			t.Fatalf("Status = %q, want %q", got.Status, PortalInviteStatusAccepted)
		}
	})

	t.Run("most recently accepted invite wins when a customer was re-invited", func(t *testing.T) {
		customerUUID := "00000000-0000-0000-0000-000000000004"
		_ = mkAcceptedInvite(t, customerUUID)
		time.Sleep(10 * time.Millisecond) // force a distinguishable accepted_at
		second := mkAcceptedInvite(t, customerUUID)

		got, err := cp.PortalInviteForCustomer(ctx, tenantID, customerUUID)
		if err != nil {
			t.Fatalf("PortalInviteForCustomer: %v", err)
		}
		if got.IdentityID != second.IdentityID {
			t.Fatalf("IdentityID = %q, want the more recently accepted identity %q", got.IdentityID, second.IdentityID)
		}
	})
}
