package controllers

import (
	"testing"
	"time"

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
	assert.Contains(t, mustEmailHTML(t, req), "Your Invoice <span style=\"white-space:nowrap;\">INV-000123</span><br>", "banner heading line 1")
	assert.Contains(t, mustEmailHTML(t, req), "has been approved.", "banner heading line 2")
	assert.Contains(t, mustEmailHTML(t, req), "Your invoice INV-000123 has been approved. You can view the approved invoice in your customer portal anytime.")
	assert.Contains(t, mustEmailHTML(t, req), "Thank you for your prompt action.", "banner subtitle")
	assert.Contains(t, mustEmailHTML(t, req), ">Invoice Number<")
	assert.Contains(t, mustEmailHTML(t, req), ">$12,450.00<")
	assert.Contains(t, mustEmailHTML(t, req), ">Sep 16, 2026<")
	assert.Contains(t, mustEmailHTML(t, req), "View invoice", "button label")
	assert.Contains(t, mustEmailHTML(t, req), `href="https://app.example.com/sales/invoice/rec-1"`)
}
