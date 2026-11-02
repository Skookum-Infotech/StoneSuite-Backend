// vendorbill/store_delete.go
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// liveReferenceQueries count the live rows that reference a vendor bill. Each
// takes the bill's internal id; the message names what blocks the delete.
var liveReferenceQueries = []struct{ sql, what string }{
	{`SELECT COUNT(*) FROM vendor_bill_payment WHERE vendor_bill_id = $1 AND deleted_at IS NULL`, "live payments"},
	{`SELECT COUNT(*) FROM vendor_payment_application WHERE vendor_bill_id = $1 AND application_deleted_at IS NULL`, "live vendor payment applications"},
	{`SELECT COUNT(*) FROM vendor_credit_application WHERE vendor_bill_id = $1 AND application_deleted_at IS NULL`, "live vendor credit applications"},
	{`SELECT COUNT(*) FROM vendor_payment_refund WHERE vendor_bill_id = $1 AND refund_deleted_at IS NULL`, "live vendor payment refunds"},
}

// SoftDelete marks a vendor bill deleted (paired deleted_at/deleted_by).
// Only DRFT and VOID bills may be deleted (AD-12) -- a bill visible to an
// approver or already settled keeps its trail. Blocked while any live
// vendor_bill_payment, vendor payment application, vendor credit application
// or vendor payment refund references it -- remove those first.
//
// The status gate, the reference counts and the delete run in one transaction
// holding the bill's row lock. Every path that adds money to a bill (payment,
// vendor payment apply, vendor credit apply) takes that same lock first, so
// none can commit between the count and the delete.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete vendor bill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	l, err := LockForUpdate(ctx, tx, uuid)
	var ce ClientError
	if errors.As(err, &ce) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	for _, q := range liveReferenceQueries {
		if err := refuseIfReferenced(ctx, tx, q.sql, q.what, l.InternalID); err != nil {
			return err
		}
	}
	if l.StatusCode != draftStatusCode && l.StatusCode != voidStatusCode {
		return ClientError{Msg: "Only a draft or void vendor bill can be deleted."}
	}

	before, err := getQ(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE vendor_bill
		SET vendor_bill_deleted_at = NOW(), vendor_bill_deleted_by = $2
		WHERE vendor_bill_id = $1`,
		l.InternalID, actorOrSystem(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete vendor bill: %w", err)
	}
	if err := auditTx(ctx, tx, auditDelete, uuid, before, nil); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete vendor bill: %w", err)
	}
	return nil
}

// refuseIfReferenced runs one live-reference count and returns a ClientError
// when anything still points at the bill.
func refuseIfReferenced(ctx context.Context, tx pgx.Tx, sql, what string, billID int) error {
	var n int
	if err := tx.QueryRow(ctx, sql, billID).Scan(&n); err != nil {
		return fmt.Errorf("count %s: %w", what, err)
	}
	if n > 0 {
		return ClientError{Msg: "Cannot delete a vendor bill with " + what + "; remove them first."}
	}
	return nil
}
