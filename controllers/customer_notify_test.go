package controllers

import (
	"testing"

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
		services.RecipientTarget{UserID: "ident-1", Email: "buyer@example.com"},
		"invoice", "Invoice", "INV-000123", "rec-1")

	assert.Equal(t, "tenant-1", req.TenantID)
	assert.Equal(t, "actor-1", req.ActorUserID)
	assert.Equal(t, []services.RecipientTarget{{UserID: "ident-1", Email: "buyer@example.com"}}, req.Recipients)
	assert.Equal(t, "invoice.customer_approved", req.EventType)
	assert.Equal(t, "invoice", req.Resource)
	assert.Equal(t, "rec-1", req.ResourceID)
	assert.Equal(t, "Your Invoice INV-000123 has been approved", req.Title)
	assert.Equal(t, "Approved.", req.Body)
	assert.Equal(t, "/sales/invoice/rec-1", req.Link)
	assert.Equal(t, []string{"email"}, req.Channels)

	assert.Contains(t, req.EmailBodyHTML, "<!DOCTYPE html>")
	assert.Contains(t, req.EmailBodyHTML, ">APPROVED<", "banner pill")
	assert.Contains(t, req.EmailBodyHTML, "Your Invoice INV-000123<br>", "banner heading line 1")
	assert.Contains(t, req.EmailBodyHTML, "has been approved.", "banner heading line 2")
	assert.Contains(t, req.EmailBodyHTML, "customer portal", "message box")
	assert.Contains(t, req.EmailBodyHTML, "View invoice", "button label")
	assert.Contains(t, req.EmailBodyHTML, `href="https://app.example.com/sales/invoice/rec-1"`)
}
