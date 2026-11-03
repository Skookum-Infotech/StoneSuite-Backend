package salesorder

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// orderReference names one kind of live document that points at a sales
// order, with the query that counts them.
type orderReference struct {
	label string
	sql   string
}

// orderReferences lists every live child document that blocks deleting its
// sales order. Each query takes the order's internal id as $1.
var orderReferences = []orderReference{
	{"an invoice", `SELECT COUNT(*) FROM invoice WHERE invoice_sales_order_id = $1 AND invoice_deleted_at IS NULL`},
	{"a fabrication job", `SELECT COUNT(*) FROM fabrication_job WHERE sales_order_id = $1 AND fabrication_job_deleted_at IS NULL`},
	{"a credit memo", `SELECT COUNT(*) FROM credit_memo WHERE credit_memo_sales_order_id = $1 AND credit_memo_deleted_at IS NULL`},
	{"a child sales order", `SELECT COUNT(*) FROM sales_order WHERE sales_order_parent_id = $1 AND sales_order_deleted_at IS NULL`},
}

// refuseIfReferenced returns ErrHasDependents when any live document still
// points at the order.
func refuseIfReferenced(ctx context.Context, tx pgx.Tx, orderInternalID int) error {
	for _, ref := range orderReferences {
		var n int
		if err := tx.QueryRow(ctx, ref.sql, orderInternalID).Scan(&n); err != nil {
			return fmt.Errorf("count sales order references (%s): %w", ref.label, err)
		}
		if n > 0 {
			return fmt.Errorf("%w: %s still references it", ErrHasDependents, ref.label)
		}
	}
	return nil
}
