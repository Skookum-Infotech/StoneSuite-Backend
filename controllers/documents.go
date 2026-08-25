package controllers

import (
	"context"
	"encoding/json"
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
) (*pgxpool.Pool, docpdf.PrintableDoc, DocMeta, string, bool) {
	pool, info, identityID, ok := authRecordAccess(w, r, recordID, action)
	if !ok {
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", false
	}
	loader, ok := h.loaders[info.WorkflowKey]
	if !ok {
		fail(w, http.StatusNotFound, "This record type has no printable document.")
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", false
	}
	seller := sellerFromTenant(tenant)
	doc, meta, err := loader(r.Context(), pool, recordID, seller)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load document.")
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", false
	}
	return pool, doc, meta, identityID, true
}

// GetPDF renders the document on the fly and streams it. RBAC: <type>:read.
func (h *DocumentOps) GetPDF(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	_, doc, meta, _, ok := h.loadForRender(w, r, recordID, authz.ActionRead)
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
	pool, doc, meta, identityID, ok := h.loadForRender(w, r, recordID, authz.ActionUpdate)
	if !ok {
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
	for _, addr := range append(append([]string{}, to...), normalizeRecipients(req.CC)...) {
		if !looksLikeEmail(addr) {
			fail(w, http.StatusBadRequest, "Invalid recipient email: "+addr)
			return
		}
	}
	subject := req.Subject
	if subject == "" {
		subject = meta.DefaultSubject
	}

	// 1. Render.
	pdf, err := h.renderPDF(doc)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to render PDF.")
		return
	}
	fileName := workflow.SanitizeFileName(meta.Number + ".pdf")
	actorUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)

	// 2. Email with the PDF attached.
	if err := services.SendDocumentEmail(to, normalizeRecipients(req.CC), subject,
		documentEmailHTML(doc, req.Message),
		[]services.EmailAttachment{{FileName: fileName, ContentType: "application/pdf", Content: pdf}},
	); err != nil {
		fail(w, http.StatusBadGateway, "Failed to send email.")
		return
	}

	// 3. Record the send + audit (best-effort audit).
	sendID, err := workflow.InsertDocumentSend(r.Context(), pool, workflow.DocumentSend{
		RecordID: recordID, WorkflowKey: meta.WorkflowKey,
		SentTo: joinRecipients(to), CC: joinRecipients(normalizeRecipients(req.CC)),
		Subject: subject, SentByUserID: actorUserID,
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to record send.")
		return
	}
	_ = workflow.LogAudit(r.Context(), pool, actorUserID, "document.sent", "document_send", sendID,
		map[string]any{"recordId": recordID, "workflowKey": meta.WorkflowKey, "to": to})

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "sendId": sendID, "sentTo": to,
	})
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
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && strings.IndexByte(s[at+1:], '.') >= 0
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
