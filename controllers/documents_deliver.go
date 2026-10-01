package controllers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docpdf"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// sendError is a document-send failure carrying the HTTP status and the
// client-safe message the endpoint reports.
type sendError struct {
	Status int
	Msg    string
}

// Error implements error.
func (e *sendError) Error() string { return e.Msg }

// preparedSend is a validated document-send: the rendered-to-be document, its
// meta, and the normalized recipients/subject. Built by prepareSend.
type preparedSend struct {
	tenantID string
	recordID string
	doc      docpdf.PrintableDoc
	meta     DocMeta
	to, cc   []string
	subject  string
	message  string
}

// prepareSend validates the recipients and subject of req against the loaded
// document, falling back to the loader's default recipient and subject. It
// touches no external service, so a caller can run it before committing to a
// state change that the send is coupled to.
func prepareSend(tenantID, recordID string, doc docpdf.PrintableDoc, meta DocMeta, req sendDocRequest) (*preparedSend, *sendError) {
	to := normalizeRecipients(req.To)
	if len(to) == 0 && meta.DefaultRecipientEmail != "" {
		to = []string{meta.DefaultRecipientEmail}
	}
	if len(to) == 0 {
		return nil, &sendError{http.StatusBadRequest, "At least one recipient is required."}
	}
	cc := normalizeRecipients(req.CC)
	for _, addr := range append(append([]string{}, to...), cc...) {
		if !looksLikeEmail(addr) {
			return nil, &sendError{http.StatusBadRequest, "Invalid recipient email: " + addr}
		}
	}
	subject := req.Subject
	if subject == "" {
		subject = meta.DefaultSubject
	}
	if hasHeaderInjection(subject) {
		return nil, &sendError{http.StatusBadRequest, "Invalid subject."}
	}
	return &preparedSend{
		tenantID: tenantID, recordID: recordID, doc: doc, meta: meta,
		to: to, cc: cc, subject: subject, message: req.Message,
	}, nil
}

// deliver renders the prepared document, emails it via Notify, records the
// send and audit row, and pings the record owner. identityID is the caller's
// control-plane identity (what Notify scopes by); it returns the document_sends
// row id and what really happened to the email.
//
// The *sendError covers a send that could not be made at all (render failed,
// Notify unreachable or refusing). The EmailOutcome covers the rest: Notify
// accepts before it sends, so after recording the send deliver waits briefly
// for the first delivery attempt (services.ConfirmEmailDelivery) and reports it
// — a provider refusal such as an unverified sender domain arrives here as a
// non-Sent outcome, not as a success. The send row is kept either way: Notify
// keeps retrying a refused delivery, and the row's notification ids are how it
// is traced.
func (h *DocumentOps) deliver(ctx context.Context, pool *pgxpool.Pool, identityID, ownerUserID string, p *preparedSend) (string, services.EmailOutcome, *sendError) {
	pdf, err := h.renderPDF(p.doc)
	if err != nil {
		return "", services.EmailOutcome{}, &sendError{http.StatusInternalServerError, "Failed to render PDF."}
	}
	fileName := workflow.SanitizeFileName(p.meta.Number + ".pdf")
	p.meta.DownloadURL = DocLinkURL(p.tenantID, p.recordID)
	// actorUserID (tenant users.id) is for the document_sends row and the
	// tenant audit log below. Notify, by contrast, scopes by the control-plane
	// identity id — so identityID is what goes on the notification requests.
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)

	// Email with the PDF attached, via Notify — queue/retry/audit reliability
	// instead of a direct, unretried call. The returned notification ids are
	// stored on the send row so the async delivery outcome can be looked back
	// up from notify later.
	notifyResult, err := services.SendNotificationWithResult(ctx,
		customerSendRequest(p.tenantID, identityID, p.meta, p.recordID, p.subject, p.doc, p.message, p.to, p.cc, fileName, pdf),
	)
	if err != nil {
		slog.WarnContext(ctx, "document send: notify failed",
			"workflow", p.meta.WorkflowKey, "record", p.recordID, "error", err)
		return "", services.EmailOutcome{}, &sendError{http.StatusBadGateway, "Failed to send email."}
	}

	sendID, err := workflow.InsertDocumentSend(ctx, pool, workflow.DocumentSend{
		RecordID: p.recordID, WorkflowKey: p.meta.WorkflowKey,
		SentTo: joinRecipients(p.to), CC: joinRecipients(p.cc),
		Subject: p.subject, SentByUserID: actorUserID,
		NotifyNotificationIDs: notifyResult.NotificationIDs,
	})
	if err != nil {
		return "", services.EmailOutcome{}, &sendError{http.StatusInternalServerError, "Failed to record send."}
	}
	_ = workflow.LogAudit(ctx, pool, actorUserID, "document.sent", "document_send", sendID,
		map[string]any{"recordId": p.recordID, "workflowKey": p.meta.WorkflowKey, "to": p.to})

	ownerAddr, ownerIdentityID, ownerName := ownerSendContact(ctx, pool, ownerUserID)
	notifyOwnerOfSend(ctx, services.SendNotification, sendOwner{Email: ownerAddr, IdentityID: ownerIdentityID, Name: ownerName},
		p.tenantID, identityID, p.doc, p.meta.Number, p.meta.WorkflowKey, p.recordID, p.to, pdf, fileName)

	// Last, so recording the send and pinging the owner are not held up by the wait.
	return sendID, services.ConfirmEmailDelivery(ctx, p.tenantID, notifyResult, nil), nil
}

// loadRecordDoc loads recordID's printable document through the loader
// registered for workflowKey. The caller has already authorized the request.
func (h *DocumentOps) loadRecordDoc(ctx context.Context, pool *pgxpool.Pool, tenant *tenancy.Tenant, recordID, workflowKey string) (docpdf.PrintableDoc, DocMeta, *sendError) {
	loader, ok := h.loaders[workflowKey]
	if !ok {
		return docpdf.PrintableDoc{}, DocMeta{}, &sendError{http.StatusNotFound, "This record type has no printable document."}
	}
	doc, meta, err := loader(ctx, pool, recordID, h.sellerFromTenant(ctx, pool, tenant))
	if err != nil {
		slog.WarnContext(ctx, "document send: load failed", "workflow", workflowKey, "record", recordID, "error", err)
		return docpdf.PrintableDoc{}, DocMeta{}, &sendError{http.StatusInternalServerError, "Failed to load document."}
	}
	return doc, meta, nil
}

// preflightRecordSend checks that recordID's document can be emailed with req
// (a recipient resolves, addresses and subject are valid) without sending
// anything, so a coupled state change can be refused up front.
func (h *DocumentOps) preflightRecordSend(ctx context.Context, pool *pgxpool.Pool, tenant *tenancy.Tenant, recordID, workflowKey string, req sendDocRequest) *sendError {
	doc, meta, serr := h.loadRecordDoc(ctx, pool, tenant, recordID, workflowKey)
	if serr != nil {
		return serr
	}
	_, serr = prepareSend(tenant.ID, recordID, doc, meta, req)
	return serr
}

// sendRecord loads, validates and emails recordID's document, bypassing the
// generic-endpoint sendDisabled gate: it is for callers that own the
// authorization and the moment of sending (PO "Send to Vendor"). It returns
// the document_sends row id and the email outcome (see deliver).
func (h *DocumentOps) sendRecord(ctx context.Context, pool *pgxpool.Pool, tenant *tenancy.Tenant, identityID, ownerUserID, recordID, workflowKey string, req sendDocRequest) (string, services.EmailOutcome, *sendError) {
	doc, meta, serr := h.loadRecordDoc(ctx, pool, tenant, recordID, workflowKey)
	if serr != nil {
		return "", services.EmailOutcome{}, serr
	}
	p, serr := prepareSend(tenant.ID, recordID, doc, meta, req)
	if serr != nil {
		return "", services.EmailOutcome{}, serr
	}
	return h.deliver(ctx, pool, identityID, ownerUserID, p)
}
