package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/services"
)

func TestDocumentOps_RequiresAuth(t *testing.T) {
	h := NewDocumentOps(map[string]DocumentLoader{}, nil)
	for name, fn := range map[string]http.HandlerFunc{
		"GetPDF": h.GetPDF, "Send": h.Send, "Sends": h.Sends,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/records/x/document/pdf", nil)
			req.SetPathValue("id", "x")
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	}
}

func TestDocumentEmailHTML_UsesSharedShellWithAttachmentChip(t *testing.T) {
	doc := docpdf.PrintableDoc{Kind: "Invoice", Number: "INV-000123", Seller: docpdf.Seller{Name: "Elevation Stone"}}
	out := documentEmailHTML(doc, "", "INV-000123.pdf")

	assert.True(t, strings.HasPrefix(out, "<!DOCTYPE html>"))
	assert.Contains(t, out, "linear-gradient(135deg,#001219 0%,#005f73 15%,#0a2943 52%,#050d1a 100%)", "uses the one shared banner shell, not a separate co-branded one")
	assert.Contains(t, out, "DOCUMENT SENT", "pill badge")
	assert.Contains(t, out, "INV-000123.pdf", "attachment chip shows the file name")
	assert.Contains(t, out, "Please find your invoice INV-000123 attached.")
	assert.Contains(t, out, "Regards,<br>Elevation Stone", "tenant identity appears in the signature, not a swapped header logo")
}

func TestDocumentEmailHTML_CustomMessageOverridesDefault(t *testing.T) {
	doc := docpdf.PrintableDoc{Kind: "Estimate", Number: "EST-1", Seller: docpdf.Seller{Name: "Acme"}}
	out := documentEmailHTML(doc, "Here's your custom note.", "EST-1.pdf")

	assert.Contains(t, out, "Here&#39;s your custom note.")
	assert.NotContains(t, out, "Please find your estimate")
}

func TestSellerFromTenant_UsesDisplayName(t *testing.T) {
	// nil-safe: empty metadata yields a seller with just the display name.
	s := sellerFromTenantMeta("Acme Stone Co", "")
	assert.Equal(t, "Acme Stone Co", s.Name)
}

var _ = docpdf.Seller{}

func TestNotifyOwnerOfSend_CallsWithOwnerID(t *testing.T) {
	var gotReq services.NotificationRequest
	called := false
	notify := func(_ context.Context, req services.NotificationRequest) error {
		called = true
		gotReq = req
		return nil
	}

	notifyOwnerOfSend(context.Background(), notify, "owner@example.com", "tenant-1", "owner-1", "actor-1",
		docpdf.PrintableDoc{Kind: "INVOICE", Seller: docpdf.Seller{Name: "Acme"}},
		"INV-1", "invoice", "rec-1", []string{"bob@buyer.example"},
		[]byte("%PDF-1.4"), "INV-1.pdf")

	assert.True(t, called)
	assert.Equal(t, "tenant-1", gotReq.TenantID)
	assert.Equal(t, "actor-1", gotReq.ActorUserID)
	require.Len(t, gotReq.Recipients, 1)
	assert.Equal(t, "owner-1", gotReq.Recipients[0].UserID)
	assert.Equal(t, "owner@example.com", gotReq.Recipients[0].Email)
	assert.Equal(t, "document.sent", gotReq.EventType)
	assert.Equal(t, "invoice", gotReq.Resource)
	assert.Equal(t, "rec-1", gotReq.ResourceID)
	assert.Equal(t, "/sales/invoice/rec-1", gotReq.Link)
	assert.Equal(t, []string{"email"}, gotReq.Channels)
	require.Len(t, gotReq.Attachments, 1)
	assert.Equal(t, "INV-1.pdf", gotReq.Attachments[0].FileName)
}

func TestNotifyOwnerOfSend_UnregisteredWorkflowKey_EmptyLink(t *testing.T) {
	var gotReq services.NotificationRequest
	notify := func(_ context.Context, req services.NotificationRequest) error {
		gotReq = req
		return nil
	}

	notifyOwnerOfSend(context.Background(), notify, "owner@example.com", "tenant-1", "owner-1", "actor-1",
		docpdf.PrintableDoc{Kind: "WIDGET"}, "W-1", "not-a-resource", "rec-1",
		[]string{"bob@buyer.example"}, []byte("%PDF-1.4"), "W-1.pdf")

	assert.Empty(t, gotReq.Link)
}

func TestNotifyOwnerOfSend_NoOwnerID_DoesNotCall(t *testing.T) {
	called := false
	notify := func(_ context.Context, _ services.NotificationRequest) error {
		called = true
		return nil
	}

	notifyOwnerOfSend(context.Background(), notify, "", "tenant-1", "", "actor-1",
		docpdf.PrintableDoc{Kind: "INVOICE"}, "INV-1", "invoice", "rec-1",
		[]string{"bob@buyer.example"}, []byte("%PDF-1.4"), "INV-1.pdf")

	assert.False(t, called)
}

func TestCustomerSendRequest_OneRecipientPerToAndCCAddress(t *testing.T) {
	req := customerSendRequest("tenant-1", "actor-1", DocMeta{WorkflowKey: "salesorder"}, "rec-1", "Sales Order SO-1",
		docpdf.PrintableDoc{Kind: "SALES ORDER", Number: "SO-1", Seller: docpdf.Seller{Name: "Acme"}},
		"Please review.", []string{"buyer@example.com"}, []string{"ap@example.com"},
		"SO-1.pdf", []byte("%PDF-1.4"))

	require.Len(t, req.Recipients, 2)
	assert.Equal(t, "buyer@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "ap@example.com", req.Recipients[1].Email)
	assert.Equal(t, "tenant-1", req.TenantID)
	assert.Equal(t, "actor-1", req.ActorUserID)
	assert.Equal(t, "document.sent", req.EventType)
	assert.Equal(t, "salesorder", req.Resource)
	assert.Equal(t, "rec-1", req.ResourceID)
	assert.Equal(t, "Sales Order SO-1", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Please review.")
	assert.Contains(t, req.EmailBodyHTML, "Acme")
	assert.Equal(t, []string{"email"}, req.Channels)
	require.Len(t, req.Attachments, 1)
	assert.Equal(t, "SO-1.pdf", req.Attachments[0].FileName)
	assert.Equal(t, "application/pdf", req.Attachments[0].ContentType)
}

func TestCustomerSendRequest_NoCC_OneRecipient(t *testing.T) {
	req := customerSendRequest("tenant-1", "actor-1", DocMeta{WorkflowKey: "invoice"}, "rec-1", "Invoice INV-1",
		docpdf.PrintableDoc{Kind: "INVOICE", Number: "INV-1", Seller: docpdf.Seller{Name: "Acme"}},
		"", []string{"buyer@example.com"}, nil, "INV-1.pdf", []byte("%PDF-1.4"))

	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "buyer@example.com", req.Recipients[0].Email)
}

func TestRecordLink(t *testing.T) {
	tests := []struct {
		name     string
		resource string
		recordID string
		want     string
	}{
		{name: "sales domain resource", resource: "estimate", recordID: "rec-1", want: "/sales/estimate/rec-1"},
		{name: "purchases domain resource", resource: "vendor", recordID: "rec-2", want: "/purchases/vendor/rec-2"},
		{name: "unknown resource falls back to empty", resource: "not-a-resource", recordID: "rec-3", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, recordLink(tt.resource, tt.recordID))
		})
	}
}

func TestLooksLikeEmail(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"valid simple", "bob@buyer.example", true},
		{"valid with subdomain", "bob@mail.buyer.example", true},
		{"no at sign", "bobbuyer.example", false},
		{"at sign first", "@buyer.example", false},
		{"at sign last", "bob@", false},
		{"no dot after at", "bob@buyer", false},
		{"CRLF injection in local part", "bob\r\nBcc:evil@attacker.example@buyer.example", false},
		{"CRLF injection after address", "bob@buyer.example\r\nBcc:evil@attacker.example", false},
		{"bare LF injection", "bob@buyer.example\nBcc:evil@attacker.example", false},
		{"bare CR injection", "bob@buyer.example\rBcc:evil@attacker.example", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, looksLikeEmail(tt.addr))
		})
	}
}

func TestHasHeaderInjection(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want bool
	}{
		{"plain subject", "Invoice INV-1 for review", false},
		{"empty string", "", false},
		{"CRLF pair", "Invoice INV-1\r\nBcc: evil@attacker.example", true},
		{"bare LF", "Invoice INV-1\nBcc: evil@attacker.example", true},
		{"bare CR", "Invoice INV-1\rBcc: evil@attacker.example", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasHeaderInjection(tt.s))
		})
	}
}

func TestNotifyOwnerOfSend_NotifyErrors_DoesNotPanicOrReturnError(t *testing.T) {
	notify := func(_ context.Context, _ services.NotificationRequest) error {
		return assert.AnError
	}
	// Must not panic; notifyOwnerOfSend has no return value to check —
	// reaching this line without panicking is the assertion.
	notifyOwnerOfSend(context.Background(), notify, "owner@example.com", "tenant-1", "owner-1", "actor-1",
		docpdf.PrintableDoc{Kind: "INVOICE"}, "INV-1", "invoice", "rec-1",
		[]string{"bob@buyer.example"}, []byte("%PDF-1.4"), "INV-1.pdf")
}

func TestNotifyOwnerOfSend_EmailUsesSharedShellWithAttachmentChip(t *testing.T) {
	prev := config.AppConfig
	config.AppConfig.FrontendURL = "https://app.example.com"
	t.Cleanup(func() { config.AppConfig = prev })
	var gotReq services.NotificationRequest
	notify := func(_ context.Context, req services.NotificationRequest) error {
		gotReq = req
		return nil
	}

	notifyOwnerOfSend(context.Background(), notify, "owner@example.com", "tenant-1", "owner-1", "actor-1",
		docpdf.PrintableDoc{Kind: "Invoice", Seller: docpdf.Seller{Name: "Acme"}},
		"INV-1", "invoice", "rec-1", []string{"bob@buyer.example", "amy@buyer.example"},
		[]byte("%PDF-1.4"), "INV-1.pdf")

	assert.Contains(t, gotReq.EmailBodyHTML, "<!DOCTYPE html>")
	assert.Contains(t, gotReq.EmailBodyHTML, ">DOCUMENT SENT<", "banner pill")
	assert.Contains(t, gotReq.EmailBodyHTML, "Invoice INV-1<br>", "banner heading line 1")
	assert.Contains(t, gotReq.EmailBodyHTML, "was sent.", "banner heading line 2")
	assert.Contains(t, gotReq.EmailBodyHTML, "Sent to bob@buyer.example, amy@buyer.example.", "message box")
	assert.Contains(t, gotReq.EmailBodyHTML, "INV-1.pdf", "attachment chip")
	assert.Contains(t, gotReq.EmailBodyHTML, `href="https://app.example.com/sales/invoice/rec-1"`)
}
