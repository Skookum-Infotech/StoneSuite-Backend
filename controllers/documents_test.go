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
