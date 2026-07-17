package controllers

import (
	"net/http"
	"time"

	"stonesuite-backend/authz"
)

// paymentRefundEntry is one live refund_application row drawing on this
// payment, flattened for the reconciliation view.
type paymentRefundEntry struct {
	RefundID     string    `json:"refundId"`
	RefundNumber string    `json:"refundNumber"`
	Amount       float64   `json:"amount"`
	AppliedAt    time.Time `json:"appliedAt"`
}

// Refunds lists the live refund applications drawing on one payment's
// overpayment — a reconciliation view, not a mutation. Uses the payment's own
// IDOR guard (authPaymentByUUID) since this is payment-centric access, not
// refund-centric.
func (h *PaymentOps) Refunds(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT rf.refund_uuid, COALESCE(rf.refund_number,''), ra.application_amount, ra.application_created_at
		FROM refund_application ra
		JOIN refund rf ON rf.refund_id = ra.refund_id
		JOIN payment p ON p.payment_id = ra.payment_id
		WHERE p.payment_uuid = $1 AND ra.application_deleted_at IS NULL AND rf.refund_deleted_at IS NULL
		ORDER BY ra.application_created_at DESC`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load refunds for payment.")
		return
	}
	defer rows.Close()
	entries := []paymentRefundEntry{}
	for rows.Next() {
		var e paymentRefundEntry
		if err := rows.Scan(&e.RefundID, &e.RefundNumber, &e.Amount, &e.AppliedAt); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to read refunds for payment.")
			return
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "recordId": id, "refunds": entries})
}
