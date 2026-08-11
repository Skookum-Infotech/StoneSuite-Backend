package controllers

import (
	"net/http"
	"time"

	"stonesuite-backend/authz"
)

// vendorBillPaymentEntry is one live vendor_payment_application row against
// this vendor bill, flattened for the AP reconciliation view.
type vendorBillPaymentEntry struct {
	VendorPaymentID     string    `json:"vendorPaymentId"`
	VendorPaymentNumber string    `json:"vendorPaymentNumber"`
	Amount              float64   `json:"amount"`
	AppliedAt           time.Time `json:"appliedAt"`
}

// vendorBillRefundEntry is one live vendor_payment_refund row against this
// vendor bill.
type vendorBillRefundEntry struct {
	VendorPaymentID     string    `json:"vendorPaymentId"`
	VendorPaymentNumber string    `json:"vendorPaymentNumber"`
	Amount              float64   `json:"amount"`
	Reason              string    `json:"reason"`
	RefundedAt          time.Time `json:"refundedAt"`
}

// Payments lists the live vendor payment applications and refunds against one
// vendor bill — an AP reconciliation view, not a mutation. Uses the vendor
// bill's own IDOR guard (authVendorBillByUUID) since this is bill-centric
// access, not vendor payment.
func (h *VendorBillOps) Payments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authVendorBillByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}

	appRows, err := pool.Query(r.Context(), `
		SELECT vp.vendor_payment_uuid, COALESCE(vp.vendor_payment_number,''), vpa.application_amount, vpa.application_created_at
		FROM vendor_payment_application vpa
		JOIN vendor_payment vp ON vp.vendor_payment_id = vpa.vendor_payment_id
		JOIN vendor_bill vb ON vb.vendor_bill_id = vpa.vendor_bill_id
		WHERE vb.vendor_bill_uuid = $1 AND vpa.application_deleted_at IS NULL AND vp.vendor_payment_deleted_at IS NULL
		ORDER BY vpa.application_created_at DESC`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load payments for vendor bill.")
		return
	}
	payments := []vendorBillPaymentEntry{}
	for appRows.Next() {
		var e vendorBillPaymentEntry
		if err := appRows.Scan(&e.VendorPaymentID, &e.VendorPaymentNumber, &e.Amount, &e.AppliedAt); err != nil {
			appRows.Close()
			fail(w, http.StatusInternalServerError, "Failed to read payments for vendor bill.")
			return
		}
		payments = append(payments, e)
	}
	appRows.Close()
	if err := appRows.Err(); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to read payments for vendor bill.")
		return
	}

	refundRows, err := pool.Query(r.Context(), `
		SELECT vp.vendor_payment_uuid, COALESCE(vp.vendor_payment_number,''), vpr.refund_amount, vpr.refund_reason, vpr.refund_refunded_at
		FROM vendor_payment_refund vpr
		JOIN vendor_payment vp ON vp.vendor_payment_id = vpr.vendor_payment_id
		JOIN vendor_bill vb ON vb.vendor_bill_id = vpr.vendor_bill_id
		WHERE vb.vendor_bill_uuid = $1 AND vpr.refund_deleted_at IS NULL AND vp.vendor_payment_deleted_at IS NULL
		ORDER BY vpr.refund_refunded_at DESC`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load refunds for vendor bill.")
		return
	}
	defer refundRows.Close()
	refunds := []vendorBillRefundEntry{}
	for refundRows.Next() {
		var e vendorBillRefundEntry
		if err := refundRows.Scan(&e.VendorPaymentID, &e.VendorPaymentNumber, &e.Amount, &e.Reason, &e.RefundedAt); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to read refunds for vendor bill.")
			return
		}
		refunds = append(refunds, e)
	}
	if err := refundRows.Err(); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to read refunds for vendor bill.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "recordId": id, "payments": payments, "refunds": refunds,
	})
}
