package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/config"
	"stonesuite-backend/services"
)

// TestCustomerStatusCopy guards which resource/status pairs email a customer.
func TestCustomerStatusCopy(t *testing.T) {
	cases := []struct {
		resource, status string
		want             bool
	}{
		{"sales_order", "OPEN", true},
		{"sales_order", "PART", true},
		{"sales_order", "FILL", true},
		{"sales_order", "CANC", true},
		{"sales_order", "DRFT", false},
		{"quote", "SENT", true},
		{"quote", "APPV", false},
		{"invoice", "SENT", true},
		{"invoice", "PART", true},
		{"invoice", "PAID", true},
		{"invoice", "VOID", true},
		{"invoice", "ODUE", true},
		{"invoice", "DRFT", false},
		{"payment", "DEPO", true},
		{"payment", "DRFT", false},
		{"refund", "SENT", true},
		{"refund", "DRFT", false},
		{"credit_memo", "APPV", true},
		{"credit_memo", "APPL", true},
		{"credit_memo", "DRFT", false},
		{"fabrication_job", "DRFT", false},
		{"fabrication_job", "ORCV", true},
		{"fabrication_job", "MALC", true},
		{"fabrication_job", "TMPL", true},
		{"fabrication_job", "TAPV", true},
		{"fabrication_job", "FRDY", true},
		{"fabrication_job", "CUTG", true},
		{"fabrication_job", "EDGP", true},
		{"fabrication_job", "QCPD", true},
		{"fabrication_job", "QCPS", true},
		{"fabrication_job", "RSHP", true},
		{"fabrication_job", "TRAN", true},
		{"fabrication_job", "INST", true},
		{"fabrication_job", "COMP", true},
		{"fabrication_job", "HOLD", true},
		{"fabrication_job", "CANC", true},
		{"vendor_bill", "SENT", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.resource+"/"+tc.status, func(t *testing.T) {
			c, ok := customerStatusCopy(tc.resource, tc.status)
			assert.Equal(t, tc.want, ok)
			if tc.want {
				assert.NotEmpty(t, c.Badge)
				assert.NotEmpty(t, c.Verb)
				assert.NotEmpty(t, c.Body)
			}
		})
	}
}

// TestBuildCustomerEventNotification guards the generic customer email shape.
func TestBuildCustomerEventNotification(t *testing.T) {
	prev := config.AppConfig
	config.AppConfig.FrontendURL = "https://app.example.com"
	t.Cleanup(func() { config.AppConfig = prev })

	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	recipient := services.RecipientTarget{UserID: "ident-1", Email: "buyer@example.com", Name: "Pat Customer"}

	cases := []struct {
		name       string
		resource   string
		display    string
		status     string
		amount     float64
		wantLink   string
		wantAmount bool
	}{
		{"invoice with amount", "invoice", "Invoice", "PAID", 250, "/sales/invoice/rec-1", true},
		{"zero amount omits row", "sales_order", "Sales Order", "OPEN", 0, "/sales/sales_order/rec-1", false},
		{"fabrication uses installation route", "fabrication_job", "Fabrication Job", "CUTG", 0, "/sales/installation/rec-1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := customerStatusEvent(tc.resource, tc.status, tc.display, "NUM-1", "rec-1", tc.amount, at)
			assert.True(t, ok)
			req := buildCustomerEventNotification("tenant-1", "actor-1", recipient, ev)

			assert.Equal(t, "tenant-1", req.TenantID)
			assert.Equal(t, "actor-1", req.ActorUserID)
			assert.Equal(t, []services.RecipientTarget{recipient}, req.Recipients)
			assert.Equal(t, tc.resource+".customer_status", req.EventType)
			assert.Equal(t, tc.resource, req.Resource)
			assert.Equal(t, "rec-1", req.ResourceID)
			assert.Equal(t, []string{"email"}, req.Channels)
			assert.Equal(t, tc.wantLink, req.Link)
			assert.Equal(t, "https://app.example.com"+tc.wantLink, req.Email.ActionURL)

			var labels []string
			for _, d := range req.Email.Details {
				labels = append(labels, d.Label)
			}
			assert.Equal(t, tc.wantAmount, contains(labels, "Amount"))
			assert.Contains(t, labels, tc.display+" Number")
		})
	}
}

// TestCustomerNonStatusCopy guards the event-type suffixes and non-empty copy
// of the non-status customer events.
func TestCustomerNonStatusCopy(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		copy   statusCopy
		suffix string
	}{
		{"received", customerReceivedCopy(), customerEventReceived},
		{"deleted", customerDeletedCopy(), customerEventDeleted},
		{"submitted", customerSubmittedCopy(), customerEventSubmitted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := tc.copy.event(tc.suffix, "invoice", "Invoice", "INV-1", "rec-1", 0, at)
			req := buildCustomerEventNotification("t", "a", services.RecipientTarget{UserID: "i"}, ev)
			assert.Equal(t, "invoice."+tc.suffix, req.EventType)
			assert.NotEmpty(t, req.Email.Badge)
			assert.Contains(t, req.Email.Paragraphs[0][0].Text, "invoice INV-1")
		})
	}
}

// TestBuildCustomerEventNotificationLinkOverride guards that a link override
// replaces the record route in both the in-app link and the email button.
func TestBuildCustomerEventNotificationLinkOverride(t *testing.T) {
	prev := config.AppConfig
	config.AppConfig.FrontendURL = "https://app.example.com"
	t.Cleanup(func() { config.AppConfig = prev })

	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	ev := customerSubmittedCopy().event(customerEventSubmitted, custResSalesOrder, "Order", "SO-1", "rec-1", 0, at)
	ev.LinkOverride = custLinkCreatedOrders
	req := buildCustomerEventNotification("t", "a", services.RecipientTarget{UserID: "i"}, ev)
	assert.Equal(t, "/sales/created-orders", req.Link)
	assert.Equal(t, "https://app.example.com/sales/created-orders", req.Email.ActionURL)
}

// contains reports whether s includes v.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
