package purchaseorder

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// orderReference names one kind of live document that points at a purchase
// order, with the query that counts them.
type orderReference struct {
	label string
	sql   string
}

// orderReferences lists every live child document that blocks deleting its
// purchase order. Each query takes the order's internal id as $1.
var orderReferences = []orderReference{
	{"an item receipt", `SELECT COUNT(*) FROM item_receipt WHERE purchase_order_id = $1 AND item_receipt_deleted_at IS NULL`},
	{"a vendor bill", `SELECT COUNT(*) FROM vendor_bill WHERE vendor_bill_purchase_order_id = $1 AND vendor_bill_deleted_at IS NULL`},
	{"a vendor bill conversion", `SELECT COUNT(*) FROM vendor_bill_conversion c
		JOIN vendor_bill vb ON vb.vendor_bill_id = c.vendor_bill_id AND vb.vendor_bill_deleted_at IS NULL
		WHERE c.purchase_order_id = $1`},
}

// deletableStatus reports whether an order in statusCode may still be deleted.
func deletableStatus(statusCode string) bool {
	return statusCode == draftStatusCode || statusCode == "CANC"
}

// refuseIfReferenced returns ErrHasDependents when any live document still
// points at the order.
func refuseIfReferenced(ctx context.Context, tx pgx.Tx, orderInternalID int) error {
	for _, ref := range orderReferences {
		var n int
		if err := tx.QueryRow(ctx, ref.sql, orderInternalID).Scan(&n); err != nil {
			return fmt.Errorf("count purchase order references (%s): %w", ref.label, err)
		}
		if n > 0 {
			return fmt.Errorf("%w: %s still references it", ErrHasDependents, ref.label)
		}
	}
	return nil
}
