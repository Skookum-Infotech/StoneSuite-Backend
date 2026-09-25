package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/services"
)

func TestDocumentOps_RequiresAuth(t *testing.T) {
	h := NewDocumentOps(map[string]DocumentLoader{}, nil, nil)
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

func TestDocumentEmail_MatchesDocumentDeliveredDesign(t *testing.T) {
	withEmailConfig(t)
	doc := docpdf.PrintableDoc{Kind: "INVOICE", Number: "INV-0042", Seller: docpdf.Seller{Name: "Acme Stone Co"}}
	out := mustRenderEmail(t, documentEmail(doc, "", "INV-0042.pdf", 245*1024))

	for _, want := range []string{
		">DOCUMENT SENT<", "Invoice <span style=\"white-space:nowrap;\">INV-0042</span><br>", "is on its way.", "Your document is ready to view.",
		`<strong style="color:#18181b;">Acme Stone Co</strong> has sent you invoice <a href="https://app.example.com/portal"`,
		">INV-0042</a>. Use the button below to download the PDF.",
		"INV-0042.pdf", "245 KB", ">View in portal<",
	} {
		assert.Contains(t, out, want)
	}
}

func TestDocumentEmail_SenderMessageIsKept(t *testing.T) {
	withEmailConfig(t)
	doc := docpdf.PrintableDoc{Kind: "ESTIMATE", Number: "EST-1", Seller: docpdf.Seller{Name: "Acme"}}
	out := mustRenderEmail(t, documentEmail(doc, "Here's your custom note.", "EST-1.pdf", 10))

	assert.Contains(t, out, "Here&#39;s your custom note.", "the sender's own message is still delivered")
	assert.Contains(t, out, "has sent you estimate")
}

func TestDocKindLabel(t *testing.T) {
	tests := []struct{ in, want string }{
		{"INVOICE", "Invoice"}, {"SALES ORDER", "Sales Order"}, {"Estimate", "Estimate"}, {"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, docKindLabel(tt.in), tt.in)
	}
}

// withEmailConfig points the frontend and footer config at fixed test values.
func withEmailConfig(t *testing.T) {
	t.Helper()
	prev := config.AppConfig
	config.AppConfig = config.Config{FrontendURL: "https://app.example.com", EmailBrandName: "StoneSuite", SupportEmail: "support@stonesuite.app"}
	t.Cleanup(func() { config.AppConfig = prev })
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

	notifyOwnerOfSend(context.Background(), notify, sendOwner{Email: "owner@example.com", IdentityID: "owner-1", Name: "Jane Owner"}, "tenant-1", "actor-1",
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

	notifyOwnerOfSend(context.Background(), notify, sendOwner{Email: "owner@example.com", IdentityID: "owner-1", Name: "Jane Owner"}, "tenant-1", "actor-1",
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

	notifyOwnerOfSend(context.Background(), notify, sendOwner{Email: "", IdentityID: "", Name: "Jane Owner"}, "tenant-1", "actor-1",
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
	assert.Contains(t, mustEmailHTML(t, req), "Please review.", "sender message kept")
	assert.Contains(t, mustEmailHTML(t, req), "Acme")
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
	notifyOwnerOfSend(context.Background(), notify, sendOwner{Email: "owner@example.com", IdentityID: "owner-1", Name: "Jane Owner"}, "tenant-1", "actor-1",
		docpdf.PrintableDoc{Kind: "INVOICE"}, "INV-1", "invoice", "rec-1",
		[]string{"bob@buyer.example"}, []byte("%PDF-1.4"), "INV-1.pdf")
}

func TestNotifyOwnerOfSend_MatchesOwnerDocumentSentDesign(t *testing.T) {
	withEmailConfig(t)
	var gotReq services.NotificationRequest
	notify := func(_ context.Context, req services.NotificationRequest) error {
		gotReq = req
		return nil
	}

	notifyOwnerOfSend(context.Background(), notify, sendOwner{Email: "owner@example.com", IdentityID: "owner-1", Name: "Jane Owner"}, "tenant-1", "actor-1",
		docpdf.PrintableDoc{Kind: "INVOICE", Seller: docpdf.Seller{Name: "Acme"}},
		"INV-000123", "invoice", "rec-1", []string{"pat@example.com"},
		make([]byte, 245*1024), "INV-000123.pdf")

	assert.Equal(t, "INVOICE INV-000123 sent", gotReq.Title, "in-app title unchanged")
	assert.Equal(t, "Jane Owner", gotReq.Recipients[0].Name)
	out := mustEmailHTML(t, gotReq)
	for _, want := range []string{
		">DOCUMENT SENT<", "Invoice <span style=\"white-space:nowrap;\">INV-000123</span><br>", "was sent.", "A copy has been shared with your customer.",
		"Invoice INV-000123 has been sent to pat@example.com. A copy of the PDF is below, and you can open the record anytime to check its status.",
		"INV-000123.pdf", "245 KB", ">View invoice<", `href="https://app.example.com/sales/invoice/rec-1"`,
		"You're receiving this because you&#39;re an account owner.",
	} {
		assert.Contains(t, out, want)
	}
}

// mustEmailHTML renders req's email body through the shared template.
func mustEmailHTML(t *testing.T, req services.NotificationRequest) string {
	t.Helper()
	out, err := req.EmailHTML()
	require.NoError(t, err)
	return out
}

func TestWithDownloadLink_PointsEveryLinkAtThePDF(t *testing.T) {
	prev := config.AppConfig
	config.AppConfig.FrontendURL = "https://app.example.com"
	t.Cleanup(func() { config.AppConfig = prev })
	const url = "https://api.example.com/d/signed"
	e := withDownloadLink(documentEmail(docpdf.PrintableDoc{Kind: "INVOICE", Number: "INV-1", Seller: docpdf.Seller{Name: "Acme"}}, "", "INV-1.pdf", 10), url)

	assert.Equal(t, url, e.ActionURL)
	assert.Equal(t, "Download PDF", e.ActionLabel)
	assert.Equal(t, url, e.Attachment.URL)
	assert.Equal(t, url, e.Paragraphs[0][2].Href)
}
