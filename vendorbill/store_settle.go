// vendorbill/store_settle.go — settles the outstanding balance when an
// operator manually moves a vendor bill to PAID.
package vendorbill

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// markedPaidMemo labels the ledger row created by a manual move to PAID.
const markedPaidMemo = "Marked as paid"

// settleOnPaid records a bill-owned settlement for whatever balance is still
// outstanding and recomputes the AP rollup, so a bill moved to PAID by hand
// never shows a balance due (AD-7: RecomputeBalance is the sole writer of
// amount_paid/balance_due). It is a no-op when the bill is already fully
// settled. Runs inside the caller's tx, which must already hold the bill row.
func settleOnPaid(ctx context.Context, tx pgx.Tx, uuid string, actorEmployeeID int) error {
	l, err := LockForUpdate(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if due := l.BalanceDue(); due > 0.005 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO vendor_bill_payment (vendor_bill_id, amount, memo, paid_at, created_by)
			VALUES ($1, $2, $3, CURRENT_DATE, $4)`,
			l.InternalID, due, markedPaidMemo, nullableInt(actorEmployeeID),
		); err != nil {
			return fmt.Errorf("insert marked-paid settlement: %w", err)
		}
	}
	return RecomputeBalance(ctx, tx, l, "settle_paid", actorEmployeeID)
}
