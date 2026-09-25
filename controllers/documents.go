package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/globalsearch"
	"stonesuite-backend/services"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
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
	// DownloadURL is the signed, login-free link the emailed PDF card points at.
	DownloadURL string
}

// recordLink builds a notification's deep-link path for resource+recordID
// from globalsearch's registry -- the single source of truth for a
// resource's frontend route (Domain/Module), reused here rather than
// hand-maintaining a second copy. "" (never blocking the notification it's
// building) if resource isn't a registered global-search provider.
func recordLink(resource, recordID string) string {
	for _, p := range globalsearch.All() {
		if string(p.Resource) == resource {
			return fmt.Sprintf("/%s/%s/%s", p.Domain, p.Module, recordID)
		}
	}
	return ""
}

// DocumentLoader loads a document by UUID and maps it to a PrintableDoc plus
// DocMeta. Registered per module at wiring time in main.go.
type DocumentLoader func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error)

// DocumentOps serves generic, record-keyed document endpoints (PDF + send),
// dispatching to a per-module loader resolved from the record's type.
type DocumentOps struct {
	loaders map[string]DocumentLoader
	// sendDisabled marks workflow keys that may still render/export a PDF
	// (GetPDF) but must not be emailed via Send -- e.g. purchase_order, where
	// "Send to Vendor" belongs on the vendor-facing AP documents (bill,
	// payment, credit, vendor profile) rather than the internal PO itself.
	sendDisabled map[string]bool
	// renderPDF is injectable for tests; defaults to docpdf.Render.
	renderPDF func(docpdf.PrintableDoc) ([]byte, error)
	// r2 is nil when R2 is not configured; the tenant-logo lookup is then
	// skipped (not fatal) and the tenant's plate is left out of the masthead.
	r2 *storage.Client
	// defaultLogo is the PNG shown as the tenant's logo while the tenant has
	// not uploaded one of its own; nil means no fallback (see WithDefaultLogo).
	defaultLogo []byte
	// linkTenants / linkPool back DownloadByLink (see WithPublicDownload).
	linkTenants docLinkTenants
	linkPool    func(context.Context, *tenancy.Tenant) (*pgxpool.Pool, error)
}

// NewDocumentOps constructs the handler group. sendDisabled and r2 may both
// be nil.
func NewDocumentOps(loaders map[string]DocumentLoader, sendDisabled map[string]bool, r2 *storage.Client) *DocumentOps {
	return &DocumentOps{loaders: loaders, sendDisabled: sendDisabled, renderPDF: docpdf.Render, r2: r2}
}

// WithDefaultLogo sets the logo PDFs show for a tenant that has not uploaded
// its own (Configuration -> Company Info) and returns h. Without it such a
// tenant's masthead carries the StoneSuite logo alone.
func (h *DocumentOps) WithDefaultLogo(png []byte) *DocumentOps {
	h.defaultLogo = png
	return h
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
	seller := h.sellerFromTenant(r.Context(), pool, tenant)
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
// whatever company profile fields exist in its onboarding metadata JSON,
// then additively takes the payment details from company_profile and looks
// up its logo if one has been uploaded (Configuration -> Company Info); a
// tenant with no logo of its own gets the default one, when configured
// (fallbackLogo). A missing/unreadable profile or logo is logged and skipped
// -- it must never fail document rendering.
func (h *DocumentOps) sellerFromTenant(ctx context.Context, pool *pgxpool.Pool, t *tenancy.Tenant) docpdf.Seller {
	s := sellerFromTenantMeta(t.DisplayName, t.Metadata)
	profile, err := companyprofile.Get(ctx, pool)
	if err != nil {
		slog.Warn("failed to load company profile for PDF letterhead", "error", err, "tenant", t.ID)
		return s
	}
	s.Payment = paymentFromProfile(profile)
	s.LogoPNG = fallbackLogo(profile, h.defaultLogo)
	if profile.LogoKey == "" {
		return s
	}
	r2 := h.r2.WithBucket(t.R2Bucket)
	if r2 == nil {
		return s
	}
	logo, err := r2.Get(ctx, profile.LogoKey)
	if err != nil {
		slog.Warn("failed to fetch tenant logo for PDF", "error", err, "tenant", t.ID)
		return s
	}
	s.LogoPNG = logo
	return s
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
	if h.sendDisabled[meta.WorkflowKey] {
		fail(w, http.StatusNotFound, "This record type does not support sending.")
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
	meta.DownloadURL = DocLinkURL(tenant.ID, recordID)
	// actorUserID (tenant users.id) is for the document_sends row and the
	// tenant audit log below. Notify, by contrast, scopes by the control-plane
	// identity id — so identityID is what goes on the notification requests.
	actorUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)

	// 2. Email with the PDF attached, via Notify — gets the same
	// queue/retry/audit reliability layer the owner ping below already
	// uses, instead of a direct, unretried Resend/SMTP call. The returned
	// notification ids are stored on the send row (step 3) so the async
	// delivery outcome can be looked back up from notify later.
	notifyResult, err := services.SendNotificationWithResult(r.Context(),
		customerSendRequest(tenant.ID, identityID, meta, recordID, subject, doc, req.Message, to, cc, fileName, pdf),
	)
	if err != nil {
		fail(w, http.StatusBadGateway, "Failed to send email.")
		return
	}

	// 3. Record the send + audit (best-effort audit).
	sendID, err := workflow.InsertDocumentSend(r.Context(), pool, workflow.DocumentSend{
		RecordID: recordID, WorkflowKey: meta.WorkflowKey,
		SentTo: joinRecipients(to), CC: joinRecipients(cc),
		Subject: subject, SentByUserID: actorUserID,
		NotifyNotificationIDs: notifyResult.NotificationIDs,
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to record send.")
		return
	}
	_ = workflow.LogAudit(r.Context(), pool, actorUserID, "document.sent", "document_send", sendID,
		map[string]any{"recordId": recordID, "workflowKey": meta.WorkflowKey, "to": to})

	ownerAddr, ownerIdentityID, ownerName := ownerSendContact(r.Context(), pool, ownerUserID)
	notifyOwnerOfSend(r.Context(), services.SendNotification, sendOwner{Email: ownerAddr, IdentityID: ownerIdentityID, Name: ownerName},
		tenant.ID, identityID, doc, meta.Number, meta.WorkflowKey, recordID, to, pdf, fileName)

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
	tenantID, actorIdentityID string, meta DocMeta, recordID, subject string,
	doc docpdf.PrintableDoc, message string, to, cc []string, fileName string, pdf []byte,
) services.NotificationRequest {
	recipients := make([]services.RecipientTarget, 0, len(to)+len(cc))
	for _, addr := range append(append([]string{}, to...), cc...) {
		r := services.RecipientTarget{Email: addr}
		if strings.EqualFold(addr, meta.DefaultRecipientEmail) {
			r.Name = meta.DefaultRecipientName
		}
		recipients = append(recipients, r)
	}
	req := services.NotificationRequest{
		TenantID:    tenantID,
		Recipients:  recipients,
		ActorUserID: actorIdentityID,
		EventType:   "document.sent",
		Resource:    meta.WorkflowKey,
		ResourceID:  recordID,
		Title:       subject,
		Body:        "Document sent.",
		Email:       withDownloadLink(documentEmail(doc, message, fileName, len(pdf)), meta.DownloadURL),
		Channels:    []string{"email"},
	}
	// The email's Download PDF button is the customer's copy; only attach the
	// PDF when there is no signed link to download it from.
	if meta.DownloadURL == "" {
		req.Attachments = []services.NotifyAttachment{{FileName: fileName, ContentType: "application/pdf", Content: pdf}}
	}
	return req
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

// ownerSendContact best-effort-resolves the record owner's email address and
// control-plane identity id for the owner ping below. Notify never resolves an
// email from a bare id (it owns no user directory; see services/notify.go), so
// the caller must supply the address; and it scopes the in-app bell row by the
// identity id the JWT carries, never the tenant-local users.id (see
// approvalchain/notify.go's contact type — a row keyed by users.id is one the
// bell can never find). Lookup failure is logged and swallowed, same as the
// ping itself. An empty identity id ⇒ notifyOwnerOfSend no-ops.
func ownerSendContact(ctx context.Context, pool *pgxpool.Pool, ownerUserID string) (email, identityID, name string) {
	if ownerUserID == "" {
		return "", "", ""
	}
	u, err := userstore.GetUserByID(ctx, pool, ownerUserID)
	if err != nil {
		slog.WarnContext(ctx, "documents: load owner contact for send notification failed",
			"owner_user_id", ownerUserID, "error", err)
		return "", "", ""
	}
	return u.Email, u.IdentityID, u.FullName
}
