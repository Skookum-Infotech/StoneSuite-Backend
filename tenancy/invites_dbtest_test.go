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
