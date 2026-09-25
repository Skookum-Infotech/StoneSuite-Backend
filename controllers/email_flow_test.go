package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/services"
)

// sharedTemplateMarker is unique to services/templates/email.html (its banner
// gradient); every email body on the wire must carry it.
const sharedTemplateMarker = "linear-gradient(135deg,#001219 0%,#005f73 15%,#0a2943 52%,#050d1a 100%)"

// fakeNotify starts a stand-in stonesuite-notify and points config at it. It
// returns the decoded JSON body of every create call it received.
func fakeNotify(t *testing.T) *[]map[string]any {
	t.Helper()
	var got []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		got = append(got, body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"notifications":[{"id":"n-1"}]}}`))
	}))
	t.Cleanup(server.Close)
	prev := config.AppConfig
	config.AppConfig = config.Config{
		NotifyURL: server.URL, NotifyAPIKey: "nk_test", FrontendURL: "https://app.example.com",
		EmailBrandName: "StoneSuite", SupportEmail: "support@stonesuite.app",
	}
	t.Cleanup(func() { config.AppConfig = prev })
	return &got
}

// wireRecipients returns the recipient emails of one notify create body.
func wireRecipients(body map[string]any) []string {
	var out []string
	for _, r := range body["recipients"].([]any) {
		out = append(out, r.(map[string]any)["email"].(string))
	}
	return out
}

// TestEmailFlow_EveryModuleRendersTheSharedTemplateForTheRightRecipient drives
// each email-triggering module's request (built after config is set, as at
// runtime) through the real
// services.SendNotification to a fake notify service and checks what arrives:
// the body is the one shared template populated with that flow's content, and
// it is addressed to the intended recipient(s).
func TestEmailFlow_EveryModuleRendersTheSharedTemplateForTheRightRecipient(t *testing.T) {
	doc := func(kind, number string) docpdf.PrintableDoc {
		return docpdf.PrintableDoc{Kind: kind, Number: number, Seller: docpdf.Seller{Name: "Acme Stone Co"}}
	}
	sendDoc := func(key, kind, number string) services.NotificationRequest {
		meta := DocMeta{WorkflowKey: key, DefaultRecipientEmail: "buyer@example.com", DefaultRecipientName: "Pat Customer"}
		return customerSendRequest("tenant-1", "actor-1", meta, "rec-1", kind+" "+number,
			doc(kind, number), "", []string{"buyer@example.com"}, []string{"ap@example.com"},
			number+".pdf", []byte("%PDF-1.4"))
	}

	tests := []struct {
		name       string
		req        func() services.NotificationRequest
		recipients []string
		subject    string
		contains   []string
	}{
		{
			name: "customer created: welcome",
			req: func() services.NotificationRequest {
				return customerWelcomeRequest("tenant-1", "Acme Stone Co", "rec-1", "bob@buyer.example", "Bob Buyer")
			},
			recipients: []string{"bob@buyer.example"},
			subject:    "Welcome to Acme Stone Co!",
			contains:   []string{">WELCOME<", ">Hello Bob Buyer,<", "Thank you for choosing", ">Go to portal<", "customer of Acme Stone Co."},
		},
		{
			name:       "invoice sent",
			req:        func() services.NotificationRequest { return sendDoc("invoice", "Invoice", "INV-000123") },
			recipients: []string{"buyer@example.com", "ap@example.com"},
			subject:    "Invoice INV-000123",
			contains:   []string{">DOCUMENT SENT<", "Invoice <span style=\"white-space:nowrap;\">INV-000123</span><br>", ">Hello Pat Customer,<", "has sent you invoice", "INV-000123.pdf", ">View in portal<"},
		},
		{
			name:       "estimate sent",
			req:        func() services.NotificationRequest { return sendDoc("estimate", "Estimate", "EST-7") },
			recipients: []string{"buyer@example.com", "ap@example.com"},
			subject:    "Estimate EST-7",
			contains:   []string{"has sent you estimate", "EST-7.pdf"},
		},
		{
			name:       "quote sent",
			req:        func() services.NotificationRequest { return sendDoc("quote", "Quote", "Q-42") },
			recipients: []string{"buyer@example.com", "ap@example.com"},
			subject:    "Quote Q-42",
			contains:   []string{"has sent you quote", "Q-42.pdf"},
		},
		{
			name: "customer notified of approval",
			req: func() services.NotificationRequest {
				return buildCustomerApprovedNotification("tenant-1", "actor-1",
					services.RecipientTarget{UserID: "ident-1", Email: "buyer@example.com", Name: "Pat Customer"},
					customerApproval{Resource: "invoice", DisplayName: "Invoice", Number: "INV-000123", Amount: 12450, RecordUUID: "rec-1", ApprovedAt: time.Now()})
			},
			recipients: []string{"buyer@example.com"},
			subject:    "Your Invoice INV-000123 has been approved",
			contains:   []string{">APPROVED<", ">Hello Pat Customer,<", ">$12,450.00<", ">Approved On<", "View invoice", `href="https://app.example.com/sales/invoice/rec-1"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fakeNotify(t)

			_, err := services.SendNotificationWithResult(context.Background(), tt.req())

			require.NoError(t, err)
			require.Len(t, *got, len(tt.recipients), "one create call per differently-greeted recipient")
			var recipients []string
			for _, b := range *got {
				recipients = append(recipients, wireRecipients(b)...)
			}
			assert.Equal(t, tt.recipients, recipients, "reaches the intended recipient(s)")
			body := (*got)[0]
			assert.Equal(t, tt.subject, body["title"], "notify's email subject")
			html, _ := body["emailBodyHtml"].(string)
			assert.True(t, strings.HasPrefix(html, "<!DOCTYPE html>"))
			assert.Contains(t, html, sharedTemplateMarker, "rendered from the one shared template")
			assert.Contains(t, html, `href="mailto:support@stonesuite.app"`, "shared footer populated from config")
			for _, want := range tt.contains {
				assert.Contains(t, html, want)
			}
		})
	}
}

func TestEmailFlow_OwnerPingOfDocumentSendUsesSharedTemplate(t *testing.T) {
	got := fakeNotify(t)

	notifyOwnerOfSend(context.Background(), services.SendNotification, sendOwner{Email: "owner@example.com", IdentityID: "owner-1", Name: "Jane Owner"}, "tenant-1", "actor-1",
		docpdf.PrintableDoc{Kind: "Invoice", Seller: docpdf.Seller{Name: "Acme"}},
		"INV-1", "invoice", "rec-1", []string{"bob@buyer.example"}, []byte("%PDF-1.4"), "INV-1.pdf")

	require.Len(t, *got, 1)
	body := (*got)[0]
	assert.Equal(t, []string{"owner@example.com"}, wireRecipients(body))
	html, _ := body["emailBodyHtml"].(string)
	assert.Contains(t, html, sharedTemplateMarker)
	assert.Contains(t, html, "has been sent to bob@buyer.example.")
	assert.Contains(t, html, ">Hello Jane Owner,<")
	require.Len(t, body["attachments"], 1, "the PDF still rides along")
}
