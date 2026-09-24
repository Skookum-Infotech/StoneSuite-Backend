package approvalchain

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	contacts := []contact{{UserID: "u1", Email: "a@example.com", Name: "Alex Approver"}}
	facts := recordFacts{Number: "INV-000123", Amount: "$12,450.00", OccurredAt: time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)}

	tests := []struct {
		name                             string
		note                             approvalNote
		title, body, badge, heading, cta string
		lead, dateRow, reason            string
	}{
		{"requested", noteApprovalRequested, "Invoice INV-000123 needs your approval", "Submitted for approval.", "APPROVAL NEEDED", "needs your approval.", "Review invoice",
			"Invoice INV-000123 has been submitted for your approval. Review the details below, then approve or reject it.", ">Submitted On<", "you&#39;re an approver."},
		{"reminder", noteApprovalReminder, "Invoice INV-000123 still needs your approval", "Still awaiting your sign-off.", "APPROVAL REMINDER", "still needs your approval.", "Review invoice",
			"Invoice INV-000123 is still waiting for your approval.", "", "you&#39;re an approver."},
		{"approved", noteApproved, "Invoice INV-000123 was approved", "Approved.", "APPROVED", "was approved.", "View invoice",
			"Invoice INV-000123 has been approved.", ">Approved On<", "you&#39;re the record owner."},
		{"sent back", noteSentBack, "Invoice INV-000123 was sent back", "Sent back for changes.", "SENT BACK", "was sent back.", "View invoice",
			"Invoice INV-000123 was sent back for changes.", ">Sent Back On<", "you&#39;re the record owner."},
		{"created", noteCreated, "Invoice INV-000123 was created", "Created.", "CREATED", "was created.", "View invoice",
			"Invoice INV-000123 was created successfully.", ">Created On<", "you created this record."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := buildApprovalNotification("tenant-1", "invoice.event", "actor-1", facts, ec, tt.note, contacts)

			assert.Equal(t, "tenant-1", req.TenantID)
			assert.Equal(t, "actor-1", req.ActorUserID)
			assert.Equal(t, "invoice.event", req.EventType)
			assert.Equal(t, "invoice", req.Resource)
			assert.Equal(t, "rec-1", req.ResourceID)
			assert.Equal(t, tt.title, req.Title)
			assert.Equal(t, tt.body, req.Body)
			assert.Equal(t, "/sales/invoice/rec-1", req.Link)
			assert.Equal(t, []string{"email"}, req.Channels)
			assert.Equal(t, []services.RecipientTarget{{UserID: "u1", Email: "a@example.com", Name: "Alex Approver"}}, req.Recipients)
			require.NotNil(t, req.Email)
			assert.True(t, req.Email.Greet, "SendNotification greets each recipient by name")

			e := *req.Email
			e.RecipientName = "Alex Approver"
			html, err := services.RenderEmail(e)
			require.NoError(t, err)
			assert.Contains(t, html, ">"+tt.badge+"<", "banner pill")
			assert.Contains(t, html, "Invoice INV-000123<br>", "banner heading line 1")
			assert.Contains(t, html, tt.heading, "banner heading line 2")
			assert.Contains(t, html, ">Hello Alex Approver,<")
			assert.Contains(t, html, tt.lead)
			assert.Contains(t, html, ">Invoice Number<")
			assert.Contains(t, html, ">$12,450.00<")
			if tt.dateRow != "" {
				assert.Contains(t, html, tt.dateRow)
				assert.Contains(t, html, ">Sep 16, 2026<")
			} else {
				assert.NotContains(t, html, "Sep 16, 2026")
			}
			assert.Contains(t, html, tt.cta, "button label")
			assert.Contains(t, html, `href="https://app.example.com/sales/invoice/rec-1"`, "absolute link, not the relative bell route")
			assert.Contains(t, html, "You're receiving this because "+tt.reason)
		})
	}
}

func TestBuildApprovalNotification_NoAmountOmitsRow(t *testing.T) {
	ec := EventContext{Resource: "requisition", DisplayName: "Requisition", RecordUUID: "rec-1"}

	req := buildApprovalNotification("tenant-1", "requisition.approved", "actor-1", recordFacts{Number: "REQ-1"}, ec, noteApproved, []contact{{UserID: "u1", Email: "a@example.com"}})

	assert.NotContains(t, mustEmailHTML(t, req), ">Amount<")
	assert.Contains(t, mustEmailHTML(t, req), ">Requisition Number<")
}

func TestBuildApprovalNotification_UnmappedResourceHasNoLinkOrButton(t *testing.T) {
	withFrontendURL(t, "https://app.example.com")
	ec := EventContext{Resource: "widget", DisplayName: "Widget", RecordUUID: "rec-1"}

	req := buildApprovalNotification("tenant-1", "widget.approved", "actor-1", recordFacts{Number: "W-1"}, ec, noteApproved, []contact{{UserID: "u1", Email: "a@example.com"}})

	assert.Empty(t, req.Link)
	assert.NotContains(t, mustEmailHTML(t, req), "View widget", "no route means no button")
	assert.Contains(t, mustEmailHTML(t, req), "<!DOCTYPE html>", "still a branded email")
}

func TestAmountColumns_CoverEveryApprovalModuleWithATotal(t *testing.T) {
	for key, cfg := range registry {
		if cfg.DisplayName == "" {
			continue
		}
		table := cfg.Record.Table
		if table == "requisition" || table == "fabrication_job" {
			continue // no total column
		}
		assert.Contains(t, amountColumns, table, "approval module %q has no amount column mapped", key)
	}
}

// mustEmailHTML renders req's email body through the shared template.
func mustEmailHTML(t *testing.T, req services.NotificationRequest) string {
	t.Helper()
	out, err := req.EmailHTML()
	require.NoError(t, err)
	return out
}
