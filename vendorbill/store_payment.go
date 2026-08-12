// vendorbill/store_payment.go — AD-7: the bill-owned settlement ledger CRUD.
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const paymentSelect = `
	SELECT vbp.vendor_bill_payment_uuid, vbp.amount, vbp.payment_method_id, COALESCE(pm.payment_method_name,''),
	       vbp.reference_number, vbp.memo, to_char(vbp.paid_at,'YYYY-MM-DD'), vbp.created_at
	FROM vendor_bill_payment vbp
	LEFT JOIN lkp_payment_method pm ON pm.payment_method_id = vbp.payment_method_id
	WHERE vbp.vendor_bill_id = $1 AND vbp.deleted_at IS NULL
	ORDER BY vbp.paid_at DESC, vbp.created_at DESC`

// loadPayments fetches a vendor bill's live settlement ledger by its
// internal id.
func loadPayments(ctx context.Context, pool *pgxpool.Pool, vbInternalID int) ([]BillPayment, error) {
	rows, err := pool.Query(ctx, paymentSelect, vbInternalID)
	if err != nil {
		return nil, fmt.Errorf("load vendor bill payments: %w", err)
	}
	defer rows.Close()
	out := []BillPayment{}
	for rows.Next() {
		var p BillPayment
		if err := rows.Scan(&p.ID, &p.Amount, &p.MethodID, &p.MethodName,
			&p.ReferenceNumber, &p.Memo, &p.PaidAt, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan vendor bill payment: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecordPaymentInput is the request payload for POST /{uuid}/payment.
type RecordPaymentInput struct {
	Amount          float64 `json:"amount"`
	MethodID        *int    `json:"methodId,omitempty"`
	ReferenceNumber string  `json:"referenceNumber"`
	Memo            string  `json:"memo"`
	PaidAt          string  `json:"paidAt"` // "yyyy-mm-dd"; blank => CURRENT_DATE
}

// RecordPayment records one settlement against a vendor bill (AD-7, AD-15):
// row-locks the bill, rejects settlement outside PayableStatuses, rejects
// overpayment (never silently clamps -- matches payment.Apply's contract),
// inserts the ledger row, and recomputes the AP rollup, all inside tx.
func RecordPayment(ctx context.Context, pool *pgxpool.Pool, uuid string, in RecordPaymentInput, actorEmployeeID int) (*VendorBill, error) {
	if in.Amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin record vendor bill payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	l, err := LockForUpdate(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if !PayableStatuses[l.StatusCode] {
		return nil, ClientError{Msg: "A vendor bill can only be paid while APPV, PART, or ODUE (current status: " + l.StatusCode + ")."}
	}
	if in.Amount > l.BalanceDue()+0.005 {
		return nil, ClientError{Msg: fmt.Sprintf("Amount %.2f exceeds the outstanding balance of %.2f.", in.Amount, l.BalanceDue())}
	}

	if in.MethodID != nil {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lkp_payment_method WHERE payment_method_id = $1)`, *in.MethodID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check payment method: %w", err)
		}
		if !exists {
			return nil, ClientError{Msg: "Unknown payment method."}
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_bill_payment (
			vendor_bill_id, payment_method_id, amount, reference_number, memo, paid_at, created_by
		) VALUES ($1,$2,$3,$4,$5, COALESCE(NULLIF($6,'')::date, CURRENT_DATE), $7)`,
		l.InternalID, in.MethodID, in.Amount, in.ReferenceNumber, in.Memo, in.PaidAt, nullableInt(actorEmployeeID),
	); err != nil {
		return nil, fmt.Errorf("insert vendor bill payment: %w", err)
	}

	if err := RecomputeBalance(ctx, tx, l, "payment", actorEmployeeID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit record vendor bill payment: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// RemovePayment soft-deletes a single ledger entry (the "unapply") and
// recomputes the AP rollup -- both inside tx.
func RemovePayment(ctx context.Context, pool *pgxpool.Pool, billUUID, paymentUUID string, actorEmployeeID int) (*VendorBill, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin remove vendor bill payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	l, err := LockForUpdate(ctx, tx, billUUID)
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE vendor_bill_payment SET deleted_at = NOW()
		WHERE vendor_bill_payment_uuid = $1 AND vendor_bill_id = $2 AND deleted_at IS NULL`,
		paymentUUID, l.InternalID)
	if err != nil {
		return nil, fmt.Errorf("remove vendor bill payment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ClientError{Msg: "Payment not found on this vendor bill."}
	}

	if err := RecomputeBalance(ctx, tx, l, "unapply_payment", actorEmployeeID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit remove vendor bill payment: %w", err)
	}
	return Get(ctx, pool, billUUID)
}

// ListPayments returns the live settlement ledger for a vendor bill -- a
// read, used by GET /{uuid}/payments.
func ListPayments(ctx context.Context, pool *pgxpool.Pool, billUUID string) ([]BillPayment, error) {
	var internalID int
	if err := pool.QueryRow(ctx,
		`SELECT vendor_bill_id FROM vendor_bill WHERE vendor_bill_uuid = $1 AND vendor_bill_deleted_at IS NULL`, billUUID,
	).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve vendor bill for payments: %w", err)
	}
	return loadPayments(ctx, pool, internalID)
}
