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
		setSalesOrderStatus(t, pool, soUUID, code)
		_, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: soUUID}, 1)
		var ce ClientError
		if !errors.As(err, &ce) {
			t.Fatalf("status %s: expected ClientError, got %T: %v", code, err, err)
		}
	}
}
