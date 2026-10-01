//go:build dbtest

package tenancy

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestUserInviteNotifyIDs exercises the notify_notification_ids column added so
// a workspace invite's async email delivery can be reconciled later. The column
// is NOT NULL DEFAULT '{}', so a fresh invite must scan as an empty (non-nil)
// slice, the setter must round-trip, a nil slice must store as '{}', a resend
// must clear it, and a setter call for a missing row must be a no-op, not an
// error.
func TestUserInviteNotifyIDs(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	mkInvite := func(t *testing.T) *UserInvite {
		t.Helper()
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		inv, err := cp.CreateUserInvite(ctx, tenantID,
			"invitee-"+suffix+"@example.com", "Invitee "+suffix, "",
			"tok-"+suffix, "", time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatalf("CreateUserInvite: %v", err)
		}
		return inv
	}

	t.Run("fresh invite scans as empty, not a scan error", func(t *testing.T) {
		inv := mkInvite(t)
		if inv.NotifyNotificationIDs == nil {
			t.Fatal("NotifyNotificationIDs is nil on the CreateUserInvite RETURNING; want empty slice")
		}
		if len(inv.NotifyNotificationIDs) != 0 {
			t.Fatalf("NotifyNotificationIDs = %v, want empty", inv.NotifyNotificationIDs)
		}
		got, err := cp.UserInviteByID(ctx, inv.ID)
		if err != nil {
			t.Fatalf("UserInviteByID: %v", err)
		}
		if len(got.NotifyNotificationIDs) != 0 {
			t.Fatalf("re-fetched NotifyNotificationIDs = %v, want empty", got.NotifyNotificationIDs)
		}
	})

	t.Run("setter round-trips", func(t *testing.T) {
		inv := mkInvite(t)
		want := []string{"n-1", "n-2"}
		if err := cp.SetUserInviteNotifyIDs(ctx, inv.ID, want); err != nil {
			t.Fatalf("SetUserInviteNotifyIDs: %v", err)
		}
		got, err := cp.UserInviteByID(ctx, inv.ID)
		if err != nil {
			t.Fatalf("UserInviteByID: %v", err)
		}
		if len(got.NotifyNotificationIDs) != 2 || got.NotifyNotificationIDs[0] != "n-1" || got.NotifyNotificationIDs[1] != "n-2" {
			t.Fatalf("NotifyNotificationIDs = %v, want %v", got.NotifyNotificationIDs, want)
		}
	})

	t.Run("nil slice stores as empty array", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetUserInviteNotifyIDs(ctx, inv.ID, []string{"n-9"}); err != nil {
			t.Fatalf("seed ids: %v", err)
		}
		if err := cp.SetUserInviteNotifyIDs(ctx, inv.ID, nil); err != nil {
			t.Fatalf("SetUserInviteNotifyIDs(nil): %v", err)
		}
		got, err := cp.UserInviteByID(ctx, inv.ID)
		if err != nil {
			t.Fatalf("UserInviteByID: %v", err)
		}
		if got.NotifyNotificationIDs == nil || len(got.NotifyNotificationIDs) != 0 {
			t.Fatalf("NotifyNotificationIDs = %v, want empty non-nil", got.NotifyNotificationIDs)
		}
	})

	t.Run("resend clears prior ids", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetUserInviteNotifyIDs(ctx, inv.ID, []string{"n-old"}); err != nil {
			t.Fatalf("seed ids: %v", err)
		}
		refreshed, err := cp.RefreshUserInvite(ctx, inv.ID, "tok-refreshed-"+inv.ID, time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatalf("RefreshUserInvite: %v", err)
		}
		if len(refreshed.NotifyNotificationIDs) != 0 {
			t.Fatalf("refreshed NotifyNotificationIDs = %v, want empty", refreshed.NotifyNotificationIDs)
		}
		got, err := cp.UserInviteByID(ctx, inv.ID)
		if err != nil {
			t.Fatalf("UserInviteByID: %v", err)
		}
		if len(got.NotifyNotificationIDs) != 0 {
			t.Fatalf("re-fetched NotifyNotificationIDs = %v, want empty", got.NotifyNotificationIDs)
		}
	})

	t.Run("list includes the ids", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetUserInviteNotifyIDs(ctx, inv.ID, []string{"n-list"}); err != nil {
			t.Fatalf("seed ids: %v", err)
		}
		invites, err := cp.ListUserInvitesByTenant(ctx, tenantID)
		if err != nil {
			t.Fatalf("ListUserInvitesByTenant: %v", err)
		}
		var found bool
		for _, i := range invites {
			if i.ID == inv.ID {
				found = true
				if len(i.NotifyNotificationIDs) != 1 || i.NotifyNotificationIDs[0] != "n-list" {
					t.Fatalf("listed NotifyNotificationIDs = %v, want [n-list]", i.NotifyNotificationIDs)
				}
			}
		}
		if !found {
			t.Fatal("ListUserInvitesByTenant did not include the seeded invite")
		}
	})

	t.Run("setter on a missing row is a no-op, not an error", func(t *testing.T) {
		const missingID = "11111111-1111-1111-1111-111111111111"
		if err := cp.SetUserInviteNotifyIDs(ctx, missingID, []string{"n-x"}); err != nil {
			t.Fatalf("SetUserInviteNotifyIDs on a missing row = %v, want nil", err)
		}
	})
}

// TestTenantInviteNotifyIDs covers the notify_notification_ids column on the
// platform onboarding invite: empty on create, round-trips through the setter,
// nil stores as '{}', a refresh (resend) clears it, a missing row is a no-op.
func TestTenantInviteNotifyIDs(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	mkInvite := func(t *testing.T) *Invite {
		t.Helper()
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		inv, err := cp.CreateInvite(ctx, tenantID, "owner-"+suffix+"@example.com", "tok-"+suffix, time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatalf("CreateInvite: %v", err)
		}
		return inv
	}

	t.Run("fresh invite scans as empty, not nil", func(t *testing.T) {
		inv := mkInvite(t)
		if inv.NotifyNotificationIDs == nil || len(inv.NotifyNotificationIDs) != 0 {
			t.Fatalf("NotifyNotificationIDs = %#v, want empty non-nil slice", inv.NotifyNotificationIDs)
		}
	})

	t.Run("setter round-trips through list and by-token reads", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetInviteNotifyIDs(ctx, inv.ID, []string{"n-1", "n-2"}); err != nil {
			t.Fatalf("SetInviteNotifyIDs: %v", err)
		}
		got, err := cp.InviteByToken(ctx, inv.Token)
		if err != nil {
			t.Fatalf("InviteByToken: %v", err)
		}
		if len(got.NotifyNotificationIDs) != 2 || got.NotifyNotificationIDs[0] != "n-1" {
			t.Fatalf("by-token ids = %v, want [n-1 n-2]", got.NotifyNotificationIDs)
		}
		list, err := cp.ListInvitesByTenant(ctx, tenantID)
		if err != nil {
			t.Fatalf("ListInvitesByTenant: %v", err)
		}
		found := false
		for _, i := range list {
			if i.ID == inv.ID {
				found = true
				if len(i.NotifyNotificationIDs) != 2 {
					t.Fatalf("listed ids = %v, want 2", i.NotifyNotificationIDs)
				}
			}
		}
		if !found {
			t.Fatal("ListInvitesByTenant did not include the invite")
		}
	})

	t.Run("nil stores as an empty array", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetInviteNotifyIDs(ctx, inv.ID, []string{"n-9"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := cp.SetInviteNotifyIDs(ctx, inv.ID, nil); err != nil {
			t.Fatalf("SetInviteNotifyIDs(nil): %v", err)
		}
		got, err := cp.InviteByToken(ctx, inv.Token)
		if err != nil || got.NotifyNotificationIDs == nil || len(got.NotifyNotificationIDs) != 0 {
			t.Fatalf("ids = %#v, err = %v; want empty non-nil", got.NotifyNotificationIDs, err)
		}
	})

	t.Run("a resend clears the previous send's ids", func(t *testing.T) {
		inv := mkInvite(t)
		if err := cp.SetInviteNotifyIDs(ctx, inv.ID, []string{"n-old"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		refreshed, err := cp.RefreshInvite(ctx, inv.ID, "tok2-"+inv.Token, time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatalf("RefreshInvite: %v", err)
		}
		if len(refreshed.NotifyNotificationIDs) != 0 {
			t.Fatalf("ids after refresh = %v, want empty", refreshed.NotifyNotificationIDs)
		}
	})

	t.Run("missing row is a no-op", func(t *testing.T) {
		if err := cp.SetInviteNotifyIDs(ctx, "00000000-0000-0000-0000-000000000000", []string{"n-1"}); err != nil {
			t.Fatalf("SetInviteNotifyIDs(missing): %v", err)
		}
	})
}
