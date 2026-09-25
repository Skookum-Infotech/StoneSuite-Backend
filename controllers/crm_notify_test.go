package controllers

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/services"
	"stonesuite-backend/workflow"
)

func TestWelcomeEmail_WellFormedWithGreeting(t *testing.T) {
	withEmailConfig(t)
	body := mustRenderEmail(t, welcomeEmail("Acme Stone Co", "Bob Buyer"))
	assert.Contains(t, body, "<!DOCTYPE html>")
	assert.NotContains(t, body, "logo-dark.png", "the remote tracking image was dropped for deliverability")
	assert.Contains(t, body, ">Hello Bob Buyer,<")
	assert.Contains(t, body, `Thank you for choosing <strong style="color:#18181b;">Acme Stone Co</strong>. Your customer portal is where you can view your sales orders, invoices, payments and refunds, and send notes to our team.`)
	assert.Contains(t, body, "We&#39;re glad to have you.", "banner subtitle")
	assert.Contains(t, body, ">Go to portal<")
	assert.Contains(t, body, "You're receiving this because you&#39;re a customer of Acme Stone Co.")
	assert.Contains(t, body, "WELCOME", "pill badge")
	assert.Contains(t, body, "linear-gradient(135deg,#001219 0%,#005f73 15%,#0a2943 52%,#050d1a 100%)", "uses the one shared template")
}

func TestWelcomeEmail_NoCustomerName_UsesGenericGreeting(t *testing.T) {
	body := mustRenderEmail(t, welcomeEmail("Acme Stone Co", ""))
	assert.True(t, strings.Contains(body, "Hello,"))
}

// mustRenderEmail renders e through the shared template.
func mustRenderEmail(t *testing.T, e *services.Email) string {
	t.Helper()
	out, err := services.RenderEmail(*e)
	require.NoError(t, err)
	return out
}

func TestNotifyCustomerWelcome_NoContactEmail_DoesNotCall(t *testing.T) {
	called := false
	notify := func(_ context.Context, _ services.NotificationRequest) error {
		called = true
		return nil
	}

	rec := &workflow.Record{ID: "rec-1", CoreFields: map[string]any{"customer_name": "Acme"}}
	notifyCustomerWelcome(context.Background(), notify, rec)

	assert.False(t, called)
}

func TestNotifyCustomerWelcome_NoTenantInContext_DoesNotCall(t *testing.T) {
	called := false
	notify := func(_ context.Context, _ services.NotificationRequest) error {
		called = true
		return nil
	}

	rec := &workflow.Record{
		ID: "rec-1",
		CoreFields: map[string]any{
			"customer_name":          "Acme",
			"customer_contact_email": "bob@buyer.example",
		},
	}
	// context.Background() carries no tenant — TenantFromContext must fail
	// and notifyCustomerWelcome must no-op rather than panic or call notify
	// with an empty tenantId.
	notifyCustomerWelcome(context.Background(), notify, rec)

	assert.False(t, called)
}
