// vendorbill/store_applied.go — the standalone vendor payments applied to a
// bill (and refunded against it): the AP reconciliation view.
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AppliedPayment is one live vendor payment application against a bill.
type AppliedPayment struct {
	VendorPaymentID     string  `json:"vendorPaymentId"`
	VendorPaymentNumber string  `json:"vendorPaymentNumber"`
	Amount              float64 `json:"amount"`
	AppliedAt           string  `json:"appliedAt"`
}

// AppliedRefund is one live vendor payment refund against a bill.
type AppliedRefund struct {
	VendorPaymentID     string  `json:"vendorPaymentId"`
	VendorPaymentNumber string  `json:"vendorPaymentNumber"`
	Amount              float64 `json:"amount"`
	Reason              string  `json:"reason"`
	RefundedAt          string  `json:"refundedAt"`
}

// Ledger is the full AP reconciliation view of a bill: applied vendor
// payments, refunds, and the bill-owned settlements (e.g. "Marked as paid").
type Ledger struct {
	Payments     []AppliedPayment `json:"payments"`
	Refunds      []AppliedRefund  `json:"refunds"`
	BillPayments []BillPayment    `json:"billPayments"`
}

// GetLedger returns a live bill's applied vendor payments, refunds and
// bill-owned settlements. Filters mirror RecomputeBalance (soft-deleted
// applications, refunds and payments are excluded).
func GetLedger(ctx context.Context, pool *pgxpool.Pool, billUUID string) (*Ledger, error) {
	var internalID int
	if err := pool.QueryRow(ctx,
		`SELECT vendor_bill_id FROM vendor_bill WHERE vendor_bill_uuid = $1 AND vendor_bill_deleted_at IS NULL`, billUUID,
	).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve vendor bill for ledger: %w", err)
	}
	l := &Ledger{Payments: []AppliedPayment{}, Refunds: []AppliedRefund{}}

	rows, err := pool.Query(ctx, `
		SELECT vp.vendor_payment_uuid::text, COALESCE(vp.vendor_payment_number,''), vpa.application_amount,
		       to_char(vpa.application_created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM vendor_payment_application vpa
		JOIN vendor_payment vp ON vp.vendor_payment_id = vpa.vendor_payment_id
		WHERE vpa.vendor_bill_id = $1 AND vpa.application_deleted_at IS NULL AND vp.vendor_payment_deleted_at IS NULL
		ORDER BY vpa.application_created_at DESC`, internalID)
	if err != nil {
		return nil, fmt.Errorf("load applied vendor payments: %w", err)
	}
	for rows.Next() {
		var a AppliedPayment
		if err := rows.Scan(&a.VendorPaymentID, &a.VendorPaymentNumber, &a.Amount, &a.AppliedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan applied vendor payment: %w", err)
		}
		l.Payments = append(l.Payments, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied vendor payments: %w", err)
	}

	rrows, err := pool.Query(ctx, `
		SELECT vp.vendor_payment_uuid::text, COALESCE(vp.vendor_payment_number,''), vpr.refund_amount, vpr.refund_reason,
		       to_char(vpr.refund_refunded_at, 'YYYY-MM-DD')
		FROM vendor_payment_refund vpr
		JOIN vendor_payment vp ON vp.vendor_payment_id = vpr.vendor_payment_id
		WHERE vpr.vendor_bill_id = $1 AND vpr.refund_deleted_at IS NULL AND vp.vendor_payment_deleted_at IS NULL
		ORDER BY vpr.refund_created_at DESC`, internalID)
	if err != nil {
		return nil, fmt.Errorf("load vendor payment refunds: %w", err)
	}
	for rrows.Next() {
		var f AppliedRefund
		if err := rrows.Scan(&f.VendorPaymentID, &f.VendorPaymentNumber, &f.Amount, &f.Reason, &f.RefundedAt); err != nil {
			rrows.Close()
			return nil, fmt.Errorf("scan vendor payment refund: %w", err)
		}
		l.Refunds = append(l.Refunds, f)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vendor payment refunds: %w", err)
	}

	if l.BillPayments, err = loadPayments(ctx, pool, internalID); err != nil {
		return nil, err
	}
	return l, nil
}
