package controllers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// DocMeta carries the non-visual metadata a loader returns alongside the
// printable document: the storage-key inputs and the default email fields.
type DocMeta struct {
	WorkflowKey           string
	Number                string
	DefaultRecipientEmail string
	DefaultRecipientName  string
	DefaultSubject        string
}

// DocumentLoader loads a document by UUID and maps it to a PrintableDoc plus
// DocMeta. Registered per module at wiring time in main.go.
type DocumentLoader func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error)

// DocumentOps serves generic, record-keyed document endpoints (PDF + send),
// dispatching to a per-module loader resolved from the record's type.
type DocumentOps struct {
	loaders map[string]DocumentLoader
	// renderPDF is injectable for tests; defaults to docpdf.Render.
	renderPDF func(docpdf.PrintableDoc) ([]byte, error)
}

// NewDocumentOps constructs the handler group.
func NewDocumentOps(loaders map[string]DocumentLoader) *DocumentOps {
	return &DocumentOps{loaders: loaders, renderPDF: docpdf.Render}
}

// loadForRender runs the shared auth gate, resolves the loader for the record's
// type, and produces the printable doc + meta. Returns ok=false having already
// written the HTTP error.
func (h *DocumentOps) loadForRender(
	w http.ResponseWriter, r *http.Request, recordID string, action authz.Action,
) (*pgxpool.Pool, docpdf.PrintableDoc, DocMeta, string, string, bool) {
	pool, info, identityID, ok := authRecordAccess(w, r, recordID, action)
	if !ok {
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", "", false
	}
	loader, ok := h.loaders[info.WorkflowKey]
	if !ok {
		fail(w, http.StatusNotFound, "This record type has no printable document.")
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", "", false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", "", false
	}
	seller := sellerFromTenant(tenant)
	doc, meta, err := loader(r.Context(), pool, recordID, seller)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load document.")
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", "", false
	}
	return pool, doc, meta, identityID, info.OwnerUserID, true
}

// GetPDF renders the document on the fly and streams it. RBAC: <type>:read.
func (h *DocumentOps) GetPDF(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	_, doc, meta, _, _, ok := h.loadForRender(w, r, recordID, authz.ActionRead)
	if !ok {
		return
	}
	pdf, err := h.renderPDF(doc)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to render PDF.")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+meta.Number+`.pdf"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// sellerFromTenant builds the letterhead from the tenant's display name and
// whatever company profile fields exist in its onboarding metadata JSON.
func sellerFromTenant(t *tenancy.Tenant) docpdf.Seller {
	return sellerFromTenantMeta(t.DisplayName, t.Metadata)
}

// sellerFromTenantMeta is the pure core of sellerFromTenant (testable without a
// tenancy.Tenant). Metadata is dynamic onboarding form JSON; unknown keys are
// simply absent.
func sellerFromTenantMeta(displayName, metadataJSON string) docpdf.Seller {
	s := docpdf.Seller{Name: displayName}
	if metadataJSON == "" {
		return s
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(metadataJSON), &m); err != nil {
		return s
	}
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	if name := get("company_name", "companyName"); name != "" {
		s.Name = name
	}
	s.AddrLine1 = get("address", "address_line1", "addressLine1", "street")
	s.CityStateZip = get("city_state_zip", "cityStateZip")
	s.Phone = get("phone", "company_phone", "phoneNumber")
	s.Email = get("email", "company_email", "contact_email")
	return s
}

// sendDocRequest is the POST .../document/send request body.
type sendDocRequest struct {
	To      []string `json:"to"`
	CC      []string `json:"cc"`
	Subject string   `json:"subject"`
	Message string   `json:"message"`
}

// Send renders the document in memory and emails it to the customer, then
// records the send. RBAC: <type>:update.
func (h *DocumentOps) Send(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, doc, meta, identityID, ownerUserID, ok := h.loadForRender(w, r, recordID, authz.ActionUpdate)
	if !ok {
		return
	}

	// Needed up front (not just for the owner ping, as before): the
	// customer email itself is now a Notify create-request, which requires
	// a real tenantId.
	tenant, tErr := tenancy.TenantFromContext(r.Context())
	if tErr != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}

	var req sendDocRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	to := normalizeRecipients(req.To)
	if len(to) == 0 && meta.DefaultRecipientEmail != "" {
		to = []string{meta.DefaultRecipientEmail}
	}
	if len(to) == 0 {
		fail(w, http.StatusBadRequest, "At least one recipient is required.")
		return
	}
	cc := normalizeRecipients(req.CC)
	for _, addr := range append(append([]string{}, to...), cc...) {
		if !looksLikeEmail(addr) {
			fail(w, http.StatusBadRequest, "Invalid recipient email: "+addr)
			return
		}
	}
	subject := req.Subject
	if subject == "" {
		subject = meta.DefaultSubject
	}
	if hasHeaderInjection(subject) {
		fail(w, http.StatusBadRequest, "Invalid subject.")
		return
	}

	// 1. Render.
	pdf, err := h.renderPDF(doc)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to render PDF.")
		return
	}
	fileName := workflow.SanitizeFileName(meta.Number + ".pdf")
	actorUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)

	// 2. Email with the PDF attached, via Notify — gets the same
	// queue/retry/audit reliability layer the owner ping below already
	// uses, instead of a direct, unretried Resend/SMTP call.
	if err := services.SendNotification(r.Context(),
		customerSendRequest(tenant.ID, meta, recordID, subject, doc, req.Message, to, cc, fileName, pdf),
	); err != nil {
		fail(w, http.StatusBadGateway, "Failed to send email.")
		return
	}

	// 3. Record the send + audit (best-effort audit).
	sendID, err := workflow.InsertDocumentSend(r.Context(), pool, workflow.DocumentSend{
		RecordID: recordID, WorkflowKey: meta.WorkflowKey,
		SentTo: joinRecipients(to), CC: joinRecipients(cc),
		Subject: subject, SentByUserID: actorUserID,
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to record send.")
		return
	}
	_ = workflow.LogAudit(r.Context(), pool, actorUserID, "document.sent", "document_send", sendID,
		map[string]any{"recordId": recordID, "workflowKey": meta.WorkflowKey, "to": to})

	notifyOwnerOfSend(r.Context(), services.SendNotification, tenant.ID, ownerUserID,
		doc, meta.Number, meta.WorkflowKey, recordID, to, pdf, fileName)

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "sendId": sendID, "sentTo": to,
	})
}

// customerSendRequest builds the Notify create request for a document-send's
// customer copy: one email-only recipient per to/cc address (Notify has no
// CC concept — each address becomes its own notification, delivery, and
// audit entry, individually addressed rather than sharing a To/Cc header),
// sharing the same branded HTML body and PDF attachment.
func customerSendRequest(
	tenantID string, meta DocMeta, recordID, subject string,
	doc docpdf.PrintableDoc, message string, to, cc []string, fileName string, pdf []byte,
) services.NotificationRequest {
	recipients := make([]services.RecipientTarget, 0, len(to)+len(cc))
	for _, addr := range append(append([]string{}, to...), cc...) {
		recipients = append(recipients, services.RecipientTarget{Email: addr})
	}
	return services.NotificationRequest{
		TenantID:      tenantID,
		Recipients:    recipients,
		EventType:     "document.sent",
		Resource:      meta.WorkflowKey,
		ResourceID:    recordID,
		Title:         subject,
		Body:          "Document sent.",
		EmailBodyHTML: documentEmailHTML(doc, message),
		Channels:      []string{"email"},
		Attachments:   []services.NotifyAttachment{{FileName: fileName, ContentType: "application/pdf", Content: pdf}},
	}
}

// Sends returns a record's document send history. RBAC: <type>:read.
func (h *DocumentOps) Sends(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, _, identityID, ok := authRecordAccess(w, r, recordID, authz.ActionRead)
	if !ok {
		return
	}
	_ = identityID
	sends, err := workflow.ListDocumentSends(r.Context(), pool, recordID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list sends.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "sends": sends})
}

// normalizeRecipients trims whitespace and drops empty entries from a
// recipient list.
func normalizeRecipients(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// joinRecipients renders a recipient list as the comma-separated string
// document_sends stores.
func joinRecipients(in []string) string { return strings.Join(in, ", ") }

// looksLikeEmail is a minimal, allocation-free sanity check (not full RFC 5322).
// Rejecting header-injection characters here is required, not just cosmetic:
// this value is forwarded unsanitized to stonesuite-notify, which builds raw
// SMTP header lines from it, and an address carrying \r\n could inject
// arbitrary extra headers or SMTP commands into the outgoing message.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at >= len(s)-1 || strings.IndexByte(s[at+1:], '.') < 0 {
		return false
	}
	return !hasHeaderInjection(s)
}

// hasHeaderInjection reports whether s contains a CR or LF byte, which would
// let it inject additional headers (or, over raw SMTP, additional commands)
// into a message assembled by simple string/Sprintf header building.
func hasHeaderInjection(s string) bool {
	return strings.ContainsAny(s, "\r\n")
}

// documentEmailHTML is the transactional email body wrapping an optional
// sender message.
func documentEmailHTML(d docpdf.PrintableDoc, message string) string {
	msg := "Please find your " + strings.ToLower(d.Kind) + " " + d.Number + " attached."
	if message != "" {
		msg = message
	}
	return `<html><body style="font-family:Arial,sans-serif;color:#333;">` +
		`<p>` + msg + `</p>` +
		`<p>Regards,<br>` + d.Seller.Name + `</p></body></html>`
}

// notifyOwnerOfSend best-effort-notifies the record's internal owner that
// the document was sent, with the same PDF the customer received attached.
// notify is injected (defaults to services.SendNotification) so tests don't
// need a live notify service. A failure here is logged and swallowed —
// the document has already been sent and recorded by the time this runs,
// and a Notify outage must never undo that.
func notifyOwnerOfSend(
	ctx context.Context,
	notify func(context.Context, services.NotificationRequest) error,
	tenantID, ownerUserID string,
	doc docpdf.PrintableDoc, number, workflowKey, recordID string,
	sentTo []string, pdf []byte, fileName string,
) {
	if ownerUserID == "" {
		return
	}
	err := notify(ctx, services.NotificationRequest{
		TenantID:   tenantID,
		Recipients: []services.RecipientTarget{{UserID: ownerUserID}},
		EventType:  "document.sent",
		Resource:   workflowKey,
		ResourceID: recordID,
		Title:      doc.Kind + " " + number + " sent",
		Body:       "Sent to " + strings.Join(sentTo, ", "),
		Channels:   []string{"email"},
		Attachments: []services.NotifyAttachment{
			{FileName: fileName, ContentType: "application/pdf", Content: pdf},
		},
	})
	if err != nil {
		slog.WarnContext(ctx, "documents: notify owner of send failed",
			"record_id", recordID, "error", err)
	}
}
