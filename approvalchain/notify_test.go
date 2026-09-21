package approvalchain

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/config"
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

func withFrontendURL(t *testing.T, url string) {
	t.Helper()
	prev := config.AppConfig
	config.AppConfig.FrontendURL = url
	t.Cleanup(func() { config.AppConfig = prev })
}

// TestBuildApprovalNotification guards that every approval-chain notification
// carries its own branded email body (the shared StoneSuite shell) instead of
// falling through to stonesuite-notify's bare generic template, and that its
// title, body and deep link are unchanged.
func TestBuildApprovalNotification(t *testing.T) {
	withFrontendURL(t, "https://app.example.com")
	ec := EventContext{Resource: "invoice", DisplayName: "Invoice", RecordUUID: "rec-1"}
	contacts := []contact{{UserID: "u1", Email: "a@example.com"}}

	tests := []struct {
		name                             string
		note                             approvalNote
		title, body, badge, heading, cta string
	}{
		{"requested", noteApprovalRequested, "Invoice INV-000123 needs your approval", "Submitted for approval.", "APPROVAL NEEDED", "needs your approval.", "Review invoice"},
		{"reminder", noteApprovalReminder, "Invoice INV-000123 still needs your approval", "Still awaiting your sign-off.", "APPROVAL REMINDER", "still needs your approval.", "Review invoice"},
		{"approved", noteApproved, "Invoice INV-000123 was approved", "Approved.", "APPROVED", "was approved.", "View invoice"},
		{"sent back", noteSentBack, "Invoice INV-000123 was sent back", "Sent back for changes.", "SENT BACK", "was sent back.", "View invoice"},
		{"created", noteCreated, "Invoice INV-000123 was created", "Created.", "CREATED", "was created.", "View invoice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := buildApprovalNotification("tenant-1", "invoice.event", "INV-000123", "actor-1", ec, tt.note, contacts)

			assert.Equal(t, "tenant-1", req.TenantID)
			assert.Equal(t, "actor-1", req.ActorUserID)
			assert.Equal(t, "invoice.event", req.EventType)
			assert.Equal(t, "invoice", req.Resource)
			assert.Equal(t, "rec-1", req.ResourceID)
			assert.Equal(t, tt.title, req.Title)
			assert.Equal(t, tt.body, req.Body)
			assert.Equal(t, "/sales/invoice/rec-1", req.Link)
			assert.Equal(t, []string{"email"}, req.Channels)
			assert.Equal(t, []services.RecipientTarget{{UserID: "u1", Email: "a@example.com"}}, req.Recipients)

			assert.Contains(t, req.EmailBodyHTML, "<!DOCTYPE html>")
			assert.Contains(t, req.EmailBodyHTML, ">"+tt.badge+"<", "banner pill")
			assert.Contains(t, req.EmailBodyHTML, "Invoice INV-000123<br>", "banner heading line 1")
			assert.Contains(t, req.EmailBodyHTML, tt.heading, "banner heading line 2")
			assert.Contains(t, req.EmailBodyHTML, tt.body, "message box")
			assert.Contains(t, req.EmailBodyHTML, tt.cta, "button label")
			assert.Contains(t, req.EmailBodyHTML, `href="https://app.example.com/sales/invoice/rec-1"`, "absolute link, not the relative bell route")
		})
	}
}

func TestBuildApprovalNotification_UnmappedResourceHasNoLinkOrButton(t *testing.T) {
	withFrontendURL(t, "https://app.example.com")
	ec := EventContext{Resource: "widget", DisplayName: "Widget", RecordUUID: "rec-1"}

	req := buildApprovalNotification("tenant-1", "widget.approved", "W-1", "actor-1", ec, noteApproved, []contact{{UserID: "u1", Email: "a@example.com"}})

	assert.Empty(t, req.Link)
	assert.NotContains(t, req.EmailBodyHTML, "View widget", "no route means no button")
	assert.Contains(t, req.EmailBodyHTML, "<!DOCTYPE html>", "still a branded email")
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
			assert.Equal(t, tt.want, rejectedNotificationBody(tt.detail))
		})
	}
}

// TestBuildApprovalNotification_RejectionReasonIsEscaped guards the seam between
// the reason-aware "sent back" wording and the branded email shell: the reason
// is approver-typed free text, so it must reach the in-app body verbatim and
// the email HTML-escaped.
func TestBuildApprovalNotification_RejectionReasonIsEscaped(t *testing.T) {
	withFrontendURL(t, "https://app.example.com")
	ec := EventContext{Resource: "invoice", DisplayName: "Invoice", RecordUUID: "rec-1", Detail: "<b>Wrong</b> amount"}
	note := noteSentBack
	note.Body = rejectedNotificationBody(ec.Detail)

	req := buildApprovalNotification("tenant-1", "invoice.approval_rejected", "INV-000123", "actor-1", ec, note, []contact{{UserID: "u1", Email: "a@example.com"}})

	assert.Equal(t, "Sent back for changes: <b>Wrong</b> amount", req.Body)
	assert.Contains(t, req.EmailBodyHTML, "Sent back for changes: &lt;b&gt;Wrong&lt;/b&gt; amount")
	assert.NotContains(t, req.EmailBodyHTML, "<b>Wrong</b>")
	// The shared sent-back note must stay untouched by the per-call override.
	assert.Equal(t, "Sent back for changes.", noteSentBack.Body)
}
