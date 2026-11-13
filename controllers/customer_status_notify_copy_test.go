package controllers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/config"
	"stonesuite-backend/services"
)

// TestCustomerStatusCopyTableIntegrity guards every table entry against a
// malformed Body format (wrong verb count would garble or panic-print the
// email) and against blank customer-facing fields.
func TestCustomerStatusCopyTableIntegrity(t *testing.T) {
	for resource, byStatus := range customerStatusCopyTable {
		for status, c := range byStatus {
			t.Run(resource+"/"+status, func(t *testing.T) {
				assert.NotEmpty(t, c.Badge)
				assert.NotEmpty(t, c.Verb)
				assert.NotEmpty(t, c.Subtitle)
				assert.NotEmpty(t, c.DateLabel)
				assert.NotEmpty(t, c.Icon)
				assert.Equal(t, 2, strings.Count(c.Body, "%s"), "Body needs exactly kind then number")
				assert.NotContains(t, c.Body, "%!", "Body has a stray format verb")
				assert.False(t, strings.HasSuffix(c.Verb, "."), "Verb must not carry the trailing period")
			})
		}
	}
	for name, c := range map[string]statusCopy{
		"received":  customerReceivedCopy(),
		"deleted":   customerDeletedCopy(),
		"submitted": customerSubmittedCopy(),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, 2, strings.Count(c.Body, "%s"))
			assert.NotEmpty(t, c.Badge)
			assert.NotEmpty(t, c.DateLabel)
		})
	}
}

// TestStatusCopyEvent guards the event built from a copy entry.
func TestStatusCopyEvent(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	ev, ok := customerStatusEvent("sales_order", "FILL", "Sales Order", "SO-9", "rec-9", 10, at)
	assert.True(t, ok)
	assert.Equal(t, "sales_order", ev.Resource)
	assert.Equal(t, customerEventStatus, ev.EventSuffix)
	assert.Equal(t, "Fulfilled", ev.Badge)
	assert.Equal(t, "Your Sales Order SO-9", ev.Heading)
	assert.Equal(t, "has been fulfilled.", ev.HeadingAccent)
	assert.Equal(t, "Your sales order SO-9 has been fully fulfilled.", ev.Body)
	assert.Equal(t, "Fulfilled On", ev.DateLabel)
	assert.Equal(t, at, ev.OccurredAt)
	assert.InDelta(t, 10, ev.Amount, 0)

	_, ok = customerStatusEvent("sales_order", "DRFT", "Sales Order", "SO-9", "rec-9", 0, at)
	assert.False(t, ok)
}

// TestCustomerEventLink guards the default record route per resource.
func TestCustomerEventLink(t *testing.T) {
	cases := []struct{ name, resource, id, want string }{
		{"standard", "invoice", "abc", "/sales/invoice/abc"},
		{"credit memo", "credit_memo", "abc", "/sales/credit_memo/abc"},
		{"fabrication remaps", "fabrication_job", "abc", "/sales/installation/abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, customerEventLink(tc.resource, tc.id))
		})
	}
}

// TestBuildCustomerEventNotificationContent guards defaults and overrides of
// the rendered email fields.
func TestBuildCustomerEventNotificationContent(t *testing.T) {
	prev := config.AppConfig
	config.AppConfig.FrontendURL = "https://app.example.com"
	t.Cleanup(func() { config.AppConfig = prev })

	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	base := customerEvent{
		Resource: "quote", DisplayName: "Quote", Number: "Q-1", RecordUUID: "rec-1",
		EventSuffix: customerEventStatus, Badge: "Ready for Review",
		Heading: "Your Quote Q-1", HeadingAccent: "is ready.", Body: "Body text", OccurredAt: at,
	}
	cases := []struct {
		name            string
		mutate          func(*customerEvent)
		wantAction      string
		wantDateLabel   string
		wantLink        string
		wantAmountValue string
	}{
		{"defaults", func(*customerEvent) {}, "View quote", "Updated On", "/sales/quote/rec-1", ""},
		{"custom action and date label", func(e *customerEvent) { e.ActionLabel = "Review now"; e.DateLabel = "Sent On" },
			"Review now", "Sent On", "/sales/quote/rec-1", ""},
		{"link override wins", func(e *customerEvent) { e.LinkOverride = "/sales/created-orders" },
			"View quote", "Updated On", "/sales/created-orders", ""},
		{"amount formatted", func(e *customerEvent) { e.Amount = 12450 },
			"View quote", "Updated On", "/sales/quote/rec-1", "$12,450.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := base
			tc.mutate(&ev)
			req := buildCustomerEventNotification("t", "a", services.RecipientTarget{UserID: "i", Email: "x@y.z"}, ev)

			assert.Equal(t, "Your Quote Q-1 is ready", req.Title)
			assert.Equal(t, "Ready for Review.", req.Body)
			assert.Equal(t, "quote.customer_status", req.EventType)
			assert.Equal(t, tc.wantLink, req.Link)
			assert.Equal(t, "https://app.example.com"+tc.wantLink, req.Email.ActionURL)
			assert.Equal(t, tc.wantAction, req.Email.ActionLabel)
			assert.Equal(t, "Your Quote Q-1 is ready.", req.Email.Preheader)
			assert.True(t, req.Email.Greet)
			assert.Equal(t, "Body text", req.Email.Paragraphs[0][0].Text)

			last := req.Email.Details[len(req.Email.Details)-1]
			assert.Equal(t, tc.wantDateLabel, last.Label)
			assert.Equal(t, "Nov 10, 2026", last.Value)
			assert.Equal(t, "Quote Number", req.Email.Details[0].Label)
			if tc.wantAmountValue != "" {
				assert.Equal(t, tc.wantAmountValue, req.Email.Details[1].Value)
			} else {
				assert.Len(t, req.Email.Details, 2)
			}
		})
	}
}

// TestNotifyCustomerGuards guards the fail-closed no-op paths: they must
// return without a tenant context, DB pool, or network, and never panic.
func TestNotifyCustomerGuards(t *testing.T) {
	ev := customerEvent{Resource: "invoice", RecordUUID: "rec-1", EventSuffix: customerEventStatus}
	cases := []struct {
		name string
		run  func()
	}{
		{"unmapped status pair", func() {
			notifyCustomerStatus(context.Background(), nil, "a", "cust", "invoice", "DRFT", "Invoice", "INV-1", 0, "rec-1")
		}},
		{"unknown resource", func() {
			notifyCustomerStatus(context.Background(), nil, "a", "cust", "vendor_bill", "SENT", "Bill", "B-1", 0, "rec-1")
		}},
		{"event without tenant", func() {
			notifyCustomerEvent(context.Background(), nil, "a", "cust", ev)
		}},
		{"to: empty recipient email", func() {
			notifyCustomerEventTo(context.Background(), "a", "cust", services.RecipientTarget{UserID: "i"}, ev)
		}},
		{"to: no tenant", func() {
			notifyCustomerEventTo(context.Background(), "a", "cust", services.RecipientTarget{UserID: "i", Email: "x@y.z"}, ev)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotPanics(t, tc.run)
		})
	}
}
