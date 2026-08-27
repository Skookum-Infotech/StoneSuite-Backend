package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docpdf"
	"stonesuite-backend/services"
)

func TestDocumentOps_RequiresAuth(t *testing.T) {
	h := NewDocumentOps(map[string]DocumentLoader{})
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

	notifyOwnerOfSend(context.Background(), notify, "tenant-1", "owner-1",
		docpdf.PrintableDoc{Kind: "INVOICE", Seller: docpdf.Seller{Name: "Acme"}},
		"INV-1", "invoice", "rec-1", []string{"bob@buyer.example"},
		[]byte("%PDF-1.4"), "INV-1.pdf")

	assert.True(t, called)
	assert.Equal(t, "tenant-1", gotReq.TenantID)
	require.Len(t, gotReq.Recipients, 1)
	assert.Equal(t, "owner-1", gotReq.Recipients[0].UserID)
	assert.Equal(t, "document.sent", gotReq.EventType)
	assert.Equal(t, "invoice", gotReq.Resource)
	assert.Equal(t, "rec-1", gotReq.ResourceID)
	assert.Equal(t, []string{"email"}, gotReq.Channels)
	require.Len(t, gotReq.Attachments, 1)
	assert.Equal(t, "INV-1.pdf", gotReq.Attachments[0].FileName)
}

func TestNotifyOwnerOfSend_NoOwnerID_DoesNotCall(t *testing.T) {
	called := false
	notify := func(_ context.Context, _ services.NotificationRequest) error {
		called = true
		return nil
	}

	notifyOwnerOfSend(context.Background(), notify, "tenant-1", "",
		docpdf.PrintableDoc{Kind: "INVOICE"}, "INV-1", "invoice", "rec-1",
		[]string{"bob@buyer.example"}, []byte("%PDF-1.4"), "INV-1.pdf")

	assert.False(t, called)
}

func TestCustomerSendRequest_OneRecipientPerToAndCCAddress(t *testing.T) {
	req := customerSendRequest("tenant-1", DocMeta{WorkflowKey: "salesorder"}, "rec-1", "Sales Order SO-1",
		docpdf.PrintableDoc{Kind: "SALES ORDER", Number: "SO-1", Seller: docpdf.Seller{Name: "Acme"}},
		"Please review.", []string{"buyer@example.com"}, []string{"ap@example.com"},
		"SO-1.pdf", []byte("%PDF-1.4"))

	require.Len(t, req.Recipients, 2)
	assert.Equal(t, "buyer@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "ap@example.com", req.Recipients[1].Email)
	assert.Equal(t, "tenant-1", req.TenantID)
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
	req := customerSendRequest("tenant-1", DocMeta{WorkflowKey: "invoice"}, "rec-1", "Invoice INV-1",
		docpdf.PrintableDoc{Kind: "INVOICE", Number: "INV-1", Seller: docpdf.Seller{Name: "Acme"}},
		"", []string{"buyer@example.com"}, nil, "INV-1.pdf", []byte("%PDF-1.4"))

	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "buyer@example.com", req.Recipients[0].Email)
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
	notifyOwnerOfSend(context.Background(), notify, "tenant-1", "owner-1",
		docpdf.PrintableDoc{Kind: "INVOICE"}, "INV-1", "invoice", "rec-1",
		[]string{"bob@buyer.example"}, []byte("%PDF-1.4"), "INV-1.pdf")
}
