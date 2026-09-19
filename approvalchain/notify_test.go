package approvalchain

import (
	"context"
	"testing"

	"stonesuite-backend/services"
)

// TestNotifyHooks_NoOpWithoutDisplayName guards the scope mechanism every
// Notify* hook relies on: an empty DisplayName must make the call a
// complete no-op -- no tenancy lookup, no DB query, no HTTP call -- so that
// engine.Approve's shared code path stays behavior-identical for every
// approvalchain-registered module that hasn't been wired for approval
// notifications (see TestRegistry_ApprovalNotificationScope). A nil pool
// proves this: any of these functions touching pool before returning would
// panic instead of silently returning.
func TestNotifyHooks_NoOpWithoutDisplayName(t *testing.T) {
	ctx := context.Background() // deliberately no tenant in context either
	ec := EventContext{
		Table: "quote", IDColumn: "quote_id", NumberColumn: "quote_number", OwnerColumn: "quote_owner_id",
		ApproverTable: "quote_approver", ApprovalTable: "quote_approval",
		RecordTypeID: 1, StatusID: 1, InternalID: 1, ActorEmployeeID: 1,
		Resource: "quote", DisplayName: "", RecordUUID: "does-not-matter",
	}

	// Each of these would panic on a nil *pgxpool.Pool if it read past the
	// DisplayName == "" guard, so simply not panicking is the assertion.
	NotifyApprovalRequested(ctx, nil, ec)
	NotifyRemainingApprovers(ctx, nil, ec)
	NotifyApproved(ctx, nil, ec)
	NotifyApprovalRejected(ctx, nil, ec)
	NotifyCreated(ctx, nil, ec)
}

func TestContactsToRecipients(t *testing.T) {
	contacts := []contact{
		{UserID: "u1", Email: "a@example.com"},
		{UserID: "u2", Email: "b@example.com"},
	}
	got := contactsToRecipients(contacts)
	want := []services.RecipientTarget{
		{UserID: "u1", Email: "a@example.com"},
		{UserID: "u2", Email: "b@example.com"},
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestContactsToRecipients_Empty(t *testing.T) {
	got := contactsToRecipients(nil)
	if len(got) != 0 {
		t.Errorf("contactsToRecipients(nil) = %v, want empty", got)
	}
}

// TestRejectedNotificationBody: the "sent back" notification says why when an
// approver gave a reason (Reject), and keeps its generic wording for the
// escapes that don't carry one (a plain void/cancel out of a gate).
func TestRejectedNotificationBody(t *testing.T) {
	tests := []struct{ name, detail, want string }{
		{"no reason", "", "Sent back for changes."},
		{"with reason", "Wrong amount", "Sent back for changes: Wrong amount"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rejectedNotificationBody(tt.detail); got != tt.want {
				t.Errorf("rejectedNotificationBody(%q) = %q, want %q", tt.detail, got, tt.want)
			}
		})
	}
}
