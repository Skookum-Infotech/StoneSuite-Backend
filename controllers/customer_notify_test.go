package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"

	"stonesuite-backend/config"
	"stonesuite-backend/services"
)

// TestBuildCustomerApprovedNotification guards that the "your document was
// approved" email a customer receives is built on the shared StoneSuite shell
// (not stonesuite-notify's bare generic template) and links to the record on
// the frontend origin — the relative bell route alone is a dead link in an inbox.
func TestBuildCustomerApprovedNotification(t *testing.T) {
	prev := config.AppConfig
	config.AppConfig.FrontendURL = "https://app.example.com"
	t.Cleanup(func() { config.AppConfig = prev })

	req := buildCustomerApprovedNotification("tenant-1", "actor-1",
		services.RecipientTarget{UserID: "ident-1", Email: "buyer@example.com", Name: "Pat Customer"},
		customerApproval{Resource: "invoice", DisplayName: "Invoice", Number: "INV-000123", Amount: 12450, RecordUUID: "rec-1",
			ApprovedAt: time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)})

	assert.Equal(t, "tenant-1", req.TenantID)
	assert.Equal(t, "actor-1", req.ActorUserID)
	assert.Equal(t, []services.RecipientTarget{{UserID: "ident-1", Email: "buyer@example.com", Name: "Pat Customer"}}, req.Recipients)
	assert.Equal(t, "invoice.customer_approved", req.EventType)
	assert.Equal(t, "invoice", req.Resource)
	assert.Equal(t, "rec-1", req.ResourceID)
	assert.Equal(t, "Your Invoice INV-000123 has been approved", req.Title)
	assert.Equal(t, "Approved.", req.Body)
	assert.Equal(t, "/sales/invoice/rec-1", req.Link)
	assert.Equal(t, []string{"email"}, req.Channels)

	assert.Contains(t, mustEmailHTML(t, req), "<!DOCTYPE html>")
	assert.Contains(t, mustEmailHTML(t, req), ">APPROVED<", "banner pill")
	assert.Contains(t, mustEmailHTML(t, req), "Your Invoice <span style=\"white-space:nowrap;\">INV-000123</span> has been approved.", "banner heading")
	assert.Contains(t, mustEmailHTML(t, req), "Your invoice INV-000123 has been approved. You can view the approved invoice in your customer portal anytime.")
	assert.Contains(t, mustEmailHTML(t, req), "Thank you for your prompt action.", "banner subtitle")
	assert.Contains(t, mustEmailHTML(t, req), ">Invoice Number<")
	assert.Contains(t, mustEmailHTML(t, req), ">$12,450.00<")
	assert.Contains(t, mustEmailHTML(t, req), ">Sep 16, 2026<")
	assert.Contains(t, mustEmailHTML(t, req), "View invoice", "button label")
	assert.Contains(t, mustEmailHTML(t, req), `href="https://app.example.com/sales/invoice/rec-1"`)
}

// TestCustomerOptedOut guards that the approval email honors the customer's
// Do Not Contact flag and fails closed when the flag can't be read.
func TestCustomerOptedOut(t *testing.T) {
	cases := []struct {
		name      string
		dnc       bool
		lookupErr error
		want      bool
	}{
		{"customer may be contacted", false, nil, false},
		{"customer flagged do not contact", true, nil, true},
		{"lookup failure skips the email", false, errors.New("db down"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(_ context.Context, _ *pgxpool.Pool, id string) (bool, error) {
				assert.Equal(t, "cust-1", id)
				return tc.dnc, tc.lookupErr
			}
			assert.Equal(t, tc.want, customerOptedOut(context.Background(), lookup, nil, "cust-1", "invoice", "rec-1"))
		})
	}
}

// TestBuildCustomerApprovedNotification_NoPortalPageForQuoteOrCreditMemo guards
// the dead-link bug: the portal has no page for a quote or credit memo, so the
// approval email must neither tell the customer to look in their portal nor
// carry a button that bounces them to Sales Orders.
func TestBuildCustomerApprovedNotification_NoPortalPageForQuoteOrCreditMemo(t *testing.T) {
	prev := config.AppConfig
	config.AppConfig.FrontendURL = "https://app.example.com"
	t.Cleanup(func() { config.AppConfig = prev })

	for _, tc := range []struct{ resource, display string }{{"quote", "Quote"}, {"credit_memo", "Credit Memo"}} {
		t.Run(tc.resource, func(t *testing.T) {
			req := buildCustomerApprovedNotification("tenant-1", "actor-1",
				services.RecipientTarget{UserID: "ident-1", Email: "buyer@example.com"},
				customerApproval{Resource: tc.resource, DisplayName: tc.display, Number: "N-1", Amount: 10, RecordUUID: "rec-1",
					ApprovedAt: time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)})

			html := mustEmailHTML(t, req)
			assert.Empty(t, req.Link)
			assert.Empty(t, req.Email.ActionURL)
			assert.NotContains(t, html, "customer portal")
			assert.NotContains(t, html, "/sales/"+tc.resource)
			assert.Contains(t, html, "Please contact us if you have any questions.")
		})
	}
}

// TestApprovedBody guards the portal wording per resource.
func TestApprovedBody(t *testing.T) {
	assert.Equal(t, "Your invoice INV-1 has been approved. You can view the approved invoice in your customer portal anytime.",
		approvedBody("invoice", "invoice", "INV-1"))
	assert.Equal(t, "Your quote Q-1 has been approved. Please contact us if you have any questions.",
		approvedBody("quote", "quote", "Q-1"))
}

// TestQuoteStatusCopyDoesNotSendCustomersToThePortal guards that the quote
// "ready for review" email makes no promise the portal cannot keep.
func TestQuoteStatusCopyDoesNotSendCustomersToThePortal(t *testing.T) {
	c, ok := customerStatusCopy(custResQuote, "SENT")
	assert.True(t, ok)
	assert.NotContains(t, c.Body, "portal")
}
