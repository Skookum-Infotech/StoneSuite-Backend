package controllers

import (
	"net/http"
	"time"

	"stonesuite-backend/authz"
)

// invoicePaymentEntry is one live payment_application row against this
// invoice, flattened for the AR reconciliation view.
type invoicePaymentEntry struct {
	PaymentID     string    `json:"paymentId"`
	PaymentNumber string    `json:"paymentNumber"`
	Amount        float64   `json:"amount"`
	AppliedAt     time.Time `json:"appliedAt"`
}

// Payments lists the live payment applications against one invoice — an AR
// reconciliation view, not a mutation. Uses the invoice's own IDOR guard
// (authInvoiceByUUID) since this is invoice-centric access, not payment.
func (h *InvoiceOps) Payments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authInvoiceByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT p.payment_uuid, COALESCE(p.payment_number,''), pa.application_amount, pa.application_created_at
		FROM payment_application pa
		JOIN payment p ON p.payment_id = pa.payment_id
		JOIN invoice i ON i.invoice_id = pa.invoice_id
		WHERE i.invoice_uuid = $1 AND pa.application_deleted_at IS NULL AND p.payment_deleted_at IS NULL
		ORDER BY pa.application_created_at DESC`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load payments for invoice.")
		return
	}
	defer rows.Close()
	entries := []invoicePaymentEntry{}
	for rows.Next() {
		var e invoicePaymentEntry
		if err := rows.Scan(&e.PaymentID, &e.PaymentNumber, &e.Amount, &e.AppliedAt); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to read payments for invoice.")
			return
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "recordId": id, "payments": entries})
}
