package controllers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// msgDoNotContact is the client message for a document send refused because
// the document's customer is flagged Do Not Contact.
const msgDoNotContact = "This customer is marked Do Not Contact. The document was not emailed."

// doNotContactLookup reports whether customerUUID is flagged Do Not Contact.
type doNotContactLookup func(ctx context.Context, pool *pgxpool.Pool, customerUUID string) (bool, error)

// checkDoNotContact refuses a customer document email when the document's
// customer is flagged Do Not Contact (409). Documents without a customer
// (vendor-side) pass. A failed lookup refuses the send (500): emailing a
// customer who opted out is worse than one failed send.
func (h *DocumentOps) checkDoNotContact(ctx context.Context, pool *pgxpool.Pool, meta DocMeta, recordID, identityID string) *sendError {
	if meta.CustomerID == "" {
		return nil
	}
	dnc, err := h.doNotContact(ctx, pool, meta.CustomerID)
	if err != nil {
		slog.ErrorContext(ctx, "document send: do-not-contact lookup failed",
			"workflow", meta.WorkflowKey, "record", recordID, "customer", meta.CustomerID, "error", err)
		return &sendError{Status: http.StatusInternalServerError, Msg: "Failed to check customer contact preference."}
	}
	if !dnc {
		return nil
	}
	slog.WarnContext(ctx, "document send refused: customer do not contact",
		"event", "document_send_do_not_contact", "workflow", meta.WorkflowKey, "record", recordID,
		"customer", meta.CustomerID, "actor_identity_id", identityID)
	return &sendError{Status: http.StatusConflict, Msg: msgDoNotContact, DoNotContact: true}
}

// writeSendError writes a document-send failure. A Do Not Contact refusal
// also carries emailSent:false so the UI never reads it as a delivery.
func writeSendError(w http.ResponseWriter, serr *sendError) {
	if !serr.DoNotContact {
		fail(w, serr.Status, serr.Msg)
		return
	}
	writeJSON(w, serr.Status, map[string]any{
		"success": false, "message": serr.Msg, "emailSent": false, "doNotContact": true,
	})
}
