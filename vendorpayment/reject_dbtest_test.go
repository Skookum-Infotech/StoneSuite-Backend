//go:build dbtest

package vendorpayment

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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
	return pool
}

// seedVendor inserts a minimal live vendor and returns its uuid.
func seedVendor(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var typeID, statusID, uuid string
	if err := pool.QueryRow(ctx, `SELECT record_type_id::text FROM lkp_record_type WHERE record_type_code = 'VNDR'`).Scan(&typeID); err != nil {
		t.Fatalf("resolve VNDR type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id::text FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'ACT_'`, typeID).Scan(&statusID); err != nil {
		t.Fatalf("resolve vendor ACT_ status: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		VALUES ($1, $2, 'Organization', 'Test Vendor Inc', 1)
		RETURNING vendor_uuid::text`, typeID, statusID).Scan(&uuid); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	return uuid
}

// configureApprover makes employee 1 the sole configured approver at the PAPV
// gate for the rest of the test.
func configureApprover(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VPAY'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve VPAY record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vendor_payment_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed vendor_payment_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM vendor_payment_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, papvStatusID)
	})
}

// pendingVendorPayment creates a vendor payment and submits it for approval.
func pendingVendorPayment(t *testing.T, pool *pgxpool.Pool) *VendorPayment {
	t.Helper()
	ctx := context.Background()
	var methodID int
	if err := pool.QueryRow(ctx, `
		SELECT payment_method_id FROM lkp_payment_method
		WHERE payment_method_deleted_at IS NULL AND payment_method_is_active ORDER BY payment_method_id LIMIT 1`).Scan(&methodID); err != nil {
		t.Fatalf("resolve an active payment method: %v", err)
	}
	created, err := Create(ctx, pool, CreateVendorPaymentInput{VendorUUID: seedVendor(t, pool), MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pending, err := Transition(ctx, pool, created.ID, "PAPV", 1)
	if err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	return pending
}

func storedRejections(t *testing.T, pool *pgxpool.Pool, uuid string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM approval_rejection ar
		JOIN vendor_payment vp ON vp.vendor_payment_id = ar.record_id
		JOIN lkp_record_type rt ON rt.record_type_id = ar.record_type_id AND rt.record_type_code = 'VPAY'
		WHERE vp.vendor_payment_uuid = $1`, uuid).Scan(&n); err != nil {
		t.Fatalf("count rejections: %v", err)
	}
	return n
}

func TestReject_SendsBackToDraftAndTheReasonClearsOnResubmit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	vp := pendingVendorPayment(t, pool)

	rejected, err := Reject(ctx, pool, vp.ID, 1, false, "Amount does not match the bill")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.StatusCode != "DRFT" {
		t.Errorf("status = %q, want DRFT", rejected.StatusCode)
	}
	if rejected.ApprovalStatus != "none" {
		t.Errorf("approval status = %q, want none", rejected.ApprovalStatus)
	}
	info, err := GetApprovalInfo(ctx, pool, vp.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.Rejection == nil || info.Rejection.Reason != "Amount does not match the bill" {
		t.Fatalf("Rejection = %+v, want the reason to be shown on the Draft it was sent back to", info.Rejection)
	}

	// Submitting it again is a new round: the old rejection goes away for good.
	if _, err := Transition(ctx, pool, vp.ID, "PAPV", 1); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if n := storedRejections(t, pool, vp.ID); n != 0 {
		t.Errorf("stored rejections = %d after resubmitting, want 0", n)
	}
	info, err = GetApprovalInfo(ctx, pool, vp.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo after resubmit: %v", err)
	}
	if info.Rejection != nil || !info.Gated || !info.CanApprove || !info.CanReject {
		t.Errorf("info = %+v, want the payment gated and open to its approver again with no rejection", info)
	}
}

func TestReject_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	vp := pendingVendorPayment(t, pool)

	if _, err := Reject(ctx, pool, vp.ID, 999999, false, "not mine to reject"); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Reject by a non-approver = %v, want ErrNotApprover", err)
	}
	if _, err := Reject(ctx, pool, "00000000-0000-0000-0000-000000000000", 1, false, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reject of a missing vendor payment = %v, want ErrNotFound", err)
	}
	if got, err := Get(ctx, pool, vp.ID); err != nil || got.StatusCode != "PAPV" {
		t.Fatalf("after refused rejects: Get = %+v, %v; want the vendor payment still at PAPV", got, err)
	}
}
