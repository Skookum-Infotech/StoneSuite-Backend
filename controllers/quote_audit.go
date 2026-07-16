// controllers/quote_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/quote"
	"stonesuite-backend/workflow"
)

// quoteSnapshot flattens an quote into a JSON-able map for the audit
// trail, mirroring soSnapshot (salesorder_audit.go) for the Quote shape.
func quoteSnapshot(e *quote.Quote) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{
		"id":          e.ID,
		"quoteNumber": e.Number,
		"status":      e.Status,
		"customerId":  e.Customer.ID,
		"grandTotal":  e.GrandTotal,
	}
}

// auditQuote records an Quote mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditSO.
func auditQuote(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldQuote, newQuote *quote.Quote) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "quote", recordID, "quote",
		quoteSnapshot(oldQuote), quoteSnapshot(newQuote), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("quote: audit %s %s: %v", action, recordID, err)
	}
}

// auditQuoteDelete is the delete-specific variant, mirroring auditSODelete.
func auditQuoteDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldQuote *quote.Quote) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "quote", recordID, "quote",
		quoteSnapshot(oldQuote), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("quote: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/quotes/{uuid}/audit
// Returns the unified audit trail for a single quote (most recent first).
func (h *QuoteOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authQuoteByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := loadAuditEntries(r.Context(), pool, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "recordId": id, "audit": entries,
	})
}
