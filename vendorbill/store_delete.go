// vendorbill/store_delete.go
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SoftDelete marks a vendor bill deleted (paired deleted_at/deleted_by).
// Only DRFT and VOID bills may be deleted (AD-12) -- a bill visible to an
// approver or already settled keeps its trail. Blocked while any live
// vendor_bill_payment references it (mirrors invoice.SoftDelete's guard on
// payment_application) -- remove the payment entries first.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	var internalID int
	if err := pool.QueryRow(ctx,
		`SELECT vendor_bill_id FROM vendor_bill WHERE vendor_bill_uuid = $1 AND vendor_bill_deleted_at IS NULL`, uuid,
	).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("resolve vendor bill for delete: %w", err)
	}

	var livePayments int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM vendor_bill_payment WHERE vendor_bill_id = $1 AND deleted_at IS NULL`,
		internalID).Scan(&livePayments); err != nil {
		return fmt.Errorf("count live vendor bill payments: %w", err)
	}
	if livePayments > 0 {
		return ClientError{Msg: "Cannot delete a vendor bill with live payments; remove them first."}
	}

	tag, err := pool.Exec(ctx, `
		UPDATE vendor_bill vb
		SET vendor_bill_deleted_at = NOW(), vendor_bill_deleted_by = $2
		FROM lkp_record_status rs
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL
		  AND rs.record_status_id = vb.vendor_bill_status
		  AND rs.record_status_code IN ('DRFT','VOID')`,
		uuid, actorOrSystem(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete vendor bill: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ClientError{Msg: "Only a draft or void vendor bill can be deleted."}
	}
	return nil
}
