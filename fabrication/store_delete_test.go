// fabrication/store_delete_test.go
//go:build dbtest

package fabrication

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/dbtestutil"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	dbtestutil.EnsureLocation(t, pool)
	return pool
}

// seedSalesOrder inserts the minimum a fabrication job needs to exist: a live
// customer and a live, confirmed (OPEN) sales order to spawn from - Create
// rejects orders that aren't approved yet. Inserted directly rather than
// via the salesorder package to keep this package's tests dependency-free.
func seedSalesOrder(t *testing.T, pool *pgxpool.Pool) (soUUID string) {
	t.Helper()
	ctx := context.Background()
	// sales_order_number is varchar(20), so keep the unique suffix short.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1e12)

	var custTypeID int
	if err := pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	var custID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_id`,
		custTypeID, "Fab Customer "+suffix).Scan(&custID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	var soTypeID, soStatusID int
	if err := pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'SORD'`).Scan(&soTypeID); err != nil {
		t.Fatalf("resolve SORD record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'OPEN'`, soTypeID).Scan(&soStatusID); err != nil {
		t.Fatalf("resolve SORD OPEN status: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO sales_order (sales_order_number, record_type, sales_order_customer_id,
		                         sales_order_status, sales_order_created_by)
		VALUES ($1, $2, $3, $4, 1) RETURNING sales_order_uuid`,
		"SO-"+suffix, soTypeID, custID, soStatusID).Scan(&soUUID); err != nil {
		t.Fatalf("seed sales order: %v", err)
	}
	return soUUID
}

// setSalesOrderStatus forces a sales order into the given status code. Job
// creation requires a confirmed order, so fixtures that build an order through
// salesorder.Create (which starts it as a Draft) call this with "OPEN".
func setSalesOrderStatus(t *testing.T, pool *pgxpool.Pool, soUUID, code string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE sales_order SET sales_order_status = (
			SELECT record_status_id FROM lkp_record_status
			WHERE record_status_record_type = sales_order.record_type AND record_status_code = $2)
		WHERE sales_order_uuid = $1`, soUUID, code); err != nil {
		t.Fatalf("set sales order status %s: %v", code, err)
	}
}

// newCancelledJob creates a job and cancels it, since SoftDelete only accepts
// draft or cancelled jobs and Create starts one at ORCV.
func newCancelledJob(t *testing.T, pool *pgxpool.Pool) *Job {
	t.Helper()
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cancelled, err := Cancel(ctx, pool, job.ID, 1)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	return cancelled
}

func TestSoftDelete_CancelledJob(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job := newCancelledJob(t, pool)

	if err := SoftDelete(ctx, pool, job.ID, 1); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
}

// TestSoftDelete_UnresolvedActor is the regression for the module-wide
// soft-delete defect: resolveEmployeeID returns 0 whenever the caller has no
// linked employee row, and binding that through nullableInt wrote SQL NULL
// into fabrication_job_deleted_by, which chk_fj_soft_delete rejects.
func TestSoftDelete_UnresolvedActor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job := newCancelledJob(t, pool)

	if err := SoftDelete(ctx, pool, job.ID, 0); err != nil {
		t.Fatalf("SoftDelete with unresolved actor: %v", err)
	}
	var deletedBy int
	if err := pool.QueryRow(ctx,
		`SELECT fabrication_job_deleted_by FROM fabrication_job WHERE fabrication_job_uuid = $1`, job.ID,
	).Scan(&deletedBy); err != nil {
		t.Fatalf("read fabrication_job_deleted_by: %v", err)
	}
	if deletedBy != systemEmployeeID {
		t.Errorf("fabrication_job_deleted_by = %d, want %d (system employee)", deletedBy, systemEmployeeID)
	}
}
