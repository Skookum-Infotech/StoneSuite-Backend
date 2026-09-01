package controllers

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/services"
	"stonesuite-backend/workflow"
)

func TestWelcomeEmailHTML_IncludesLogoAndGreeting(t *testing.T) {
	html := welcomeEmailHTML("Acme Stone Co", "Bob Buyer")
	assert.Contains(t, html, "/logo-dark.png")
	assert.Contains(t, html, "Hello Bob Buyer,")
	assert.Contains(t, html, "Welcome to Acme Stone Co!")
}

func TestWelcomeEmailHTML_NoCustomerName_UsesGenericGreeting(t *testing.T) {
	html := welcomeEmailHTML("Acme Stone Co", "")
	assert.True(t, strings.Contains(html, "Hello,"))
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
