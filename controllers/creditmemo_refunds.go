package controllers

import (
	"net/http"
	"time"

	"stonesuite-backend/authz"
)

// creditMemoRefundEntry is one live refund_application row drawing on this
// credit memo, flattened for the reconciliation view.
type creditMemoRefundEntry struct {
	RefundID     string    `json:"refundId"`
	RefundNumber string    `json:"refundNumber"`
	Amount       float64   `json:"amount"`
	AppliedAt    time.Time `json:"appliedAt"`
}

// Refunds lists the live refund applications drawing on one credit memo's
// unapplied balance — a reconciliation view, not a mutation. Uses the credit
// memo's own IDOR guard (authCreditMemoByUUID) since this is
// credit-memo-centric access, not refund-centric.
func (h *CreditMemoOps) Refunds(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authCreditMemoByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT rf.refund_uuid, COALESCE(rf.refund_number,''), ra.application_amount, ra.application_created_at
		FROM refund_application ra
		JOIN refund rf ON rf.refund_id = ra.refund_id
		JOIN credit_memo cm ON cm.credit_memo_id = ra.credit_memo_id
		WHERE cm.credit_memo_uuid = $1 AND ra.application_deleted_at IS NULL AND rf.refund_deleted_at IS NULL
		ORDER BY ra.application_created_at DESC`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load refunds for credit memo.")
		return
	}
	defer rows.Close()
	entries := []creditMemoRefundEntry{}
	for rows.Next() {
		var e creditMemoRefundEntry
		if err := rows.Scan(&e.RefundID, &e.RefundNumber, &e.Amount, &e.AppliedAt); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to read refunds for credit memo.")
			return
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "recordId": id, "refunds": entries})
}
