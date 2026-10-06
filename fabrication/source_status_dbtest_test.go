//go:build dbtest

package fabrication

import (
	"context"
	"errors"
	"testing"
)

// A job can only be opened from a confirmed sales order: Draft, Pending
// Approval and Cancelled orders are rejected with a ClientError (400).
func TestCreate_RequiresConfirmedSalesOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soUUID := seedSalesOrder(t, pool)

	for _, code := range []string{"DRFT", "PAPV", "CANC"} {
		if _, err := pool.Exec(ctx, `
			UPDATE sales_order SET sales_order_status = (
				SELECT record_status_id FROM lkp_record_status
				WHERE record_status_record_type = sales_order.record_type AND record_status_code = $2)
			WHERE sales_order_uuid = $1`, soUUID, code); err != nil {
			t.Fatalf("set status %s: %v", code, err)
		}
		_, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: soUUID}, 1)
		var ce ClientError
		if !errors.As(err, &ce) {
			t.Fatalf("status %s: expected ClientError, got %T: %v", code, err, err)
		}
	}
}
