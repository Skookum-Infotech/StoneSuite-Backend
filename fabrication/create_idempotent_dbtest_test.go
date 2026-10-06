//go:build dbtest

package fabrication

import (
	"context"
	"errors"
	"testing"
)

func countJobsForOrder(t *testing.T, soUUID string) int {
	t.Helper()
	var n int
	if err := testPool(t).QueryRow(context.Background(), `
		SELECT COUNT(*) FROM fabrication_job fj JOIN sales_order so ON so.sales_order_id = fj.sales_order_id
		WHERE so.sales_order_uuid = $1 AND fj.fabrication_job_deleted_at IS NULL`, soUUID).Scan(&n); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return n
}

// A retried create with the same requestId returns the first job; without one,
// every call opens a new job (a sales order may legitimately have several).
func TestCreateOnce_RetryWithRequestIDReplays(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soUUID := seedSalesOrder(t, pool)
	in := CreateJobInput{SalesOrderUUID: soUUID, RequestID: "req-1"}

	first, created, err := CreateOnce(ctx, pool, in, 1)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	again, created, err := CreateOnce(ctx, pool, in, 1)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("retry: created=%v err=%v id=%v want %v", created, err, again, first.ID)
	}
	if n := countJobsForOrder(t, soUUID); n != 1 {
		t.Fatalf("jobs after retry = %d, want 1", n)
	}

	// A different key is a genuinely new request.
	in.RequestID = "req-2"
	if _, created, err := CreateOnce(ctx, pool, in, 1); err != nil || !created {
		t.Fatalf("new key: created=%v err=%v", created, err)
	}
	// No key: no dedupe.
	in.RequestID = ""
	if _, created, err := CreateOnce(ctx, pool, in, 1); err != nil || !created {
		t.Fatalf("no key: created=%v err=%v", created, err)
	}
	if n := countJobsForOrder(t, soUUID); n != 3 {
		t.Fatalf("jobs = %d, want 3", n)
	}
}

// The replay still returns the job after the order was cancelled meanwhile, and
// another caller reusing the key does not receive someone else's job.
func TestCreateOnce_ReplayIsScopedToCaller(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soUUID := seedSalesOrder(t, pool)
	in := CreateJobInput{SalesOrderUUID: soUUID, RequestID: "req-shared"}
	first, _, err := CreateOnce(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setSalesOrderStatus(t, pool, soUUID, "CANC")
	again, created, err := CreateOnce(ctx, pool, in, 1)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay after cancel: created=%v err=%v", created, err)
	}

	_, _, err = CreateOnce(ctx, pool, in, 2)
	var ce ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("other caller, same key: want ClientError, got %T: %v", err, err)
	}
}
