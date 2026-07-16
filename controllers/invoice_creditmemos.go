package controllers

import (
	"net/http"
	"time"

	"stonesuite-backend/authz"
)

// invoiceCreditEntry is one live credit_memo_application row against this
// invoice, flattened for the AR reconciliation view.
type invoiceCreditEntry struct {
	CreditMemoID     string    `json:"creditMemoId"`
	CreditMemoNumber string    `json:"creditMemoNumber"`
	Reason           string    `json:"reason"`
	Amount           float64   `json:"amount"`
	AppliedAt        time.Time `json:"appliedAt"`
}

// CreditMemos lists the live credit memo applications against one invoice — an
// AR reconciliation view, not a mutation. Sibling of Payments: together they
// account for the whole of an invoice's balance_due
// (grand_total - amount_paid - credit_total). Uses the invoice's own IDOR guard
// since this is invoice-centric access, not credit-memo-centric.
func (h *InvoiceOps) CreditMemos(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authInvoiceByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT cm.credit_memo_uuid, COALESCE(cm.credit_memo_number,''), cm.credit_memo_reason,
		       ca.application_amount, ca.application_created_at
		FROM credit_memo_application ca
		JOIN credit_memo cm ON cm.credit_memo_id = ca.credit_memo_id
		JOIN invoice i ON i.invoice_id = ca.invoice_id
		WHERE i.invoice_uuid = $1 AND ca.application_deleted_at IS NULL AND cm.credit_memo_deleted_at IS NULL
		ORDER BY ca.application_created_at DESC`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load credit memos for invoice.")
		return
	}
	defer rows.Close()
	entries := []invoiceCreditEntry{}
	for rows.Next() {
		var e invoiceCreditEntry
		if err := rows.Scan(&e.CreditMemoID, &e.CreditMemoNumber, &e.Reason, &e.Amount, &e.AppliedAt); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to read credit memos for invoice.")
			return
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "recordId": id, "creditMemos": entries})
}
