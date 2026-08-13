//go:build dbtest

package vendorcredit

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/vendorbill"
	"stonesuite-backend/vendorpayment"
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

// seedVendorWithStatus inserts a minimal live vendor with the given
// lkp_record_status code (scoped to VNDR) and returns its uuid.
func seedVendorWithStatus(t *testing.T, pool *pgxpool.Pool, statusCode string) string {
	t.Helper()
	ctx := context.Background()
	var typeID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VNDR'`).Scan(&typeID); err != nil {
		t.Fatalf("resolve VNDR type: %v", err)
	}
	var statusID int
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, statusCode).Scan(&statusID); err != nil {
		t.Fatalf("resolve vendor %s status: %v", statusCode, err)
	}
	var uuid string
	if err := pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		VALUES ($1, $2, 'Organization', 'Test Vendor Inc', 1)
		RETURNING vendor_uuid::text`, typeID, statusID).Scan(&uuid); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	return uuid
}

// seedVendor inserts a minimal live, ACTIVE vendor and returns its uuid.
func seedVendor(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	return seedVendorWithStatus(t, pool, "ACT_")
}

// seedInactiveVendor inserts a live vendor with INA_ status (AD-8's gate) and
// returns its uuid.
func seedInactiveVendor(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	return seedVendorWithStatus(t, pool, "INA_")
}

// seedDeletedVendor inserts an active vendor and immediately soft-deletes it,
// exercising activeVendorSnapshot's "Unknown vendor." path (distinct from the
// "Vendor is not active." path seedInactiveVendor exercises).
func seedDeletedVendor(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	uuid := seedVendor(t, pool)
	if _, err := pool.Exec(ctx,
		`UPDATE vendor SET vendor_deleted_at = NOW(), vendor_deleted_by = 1 WHERE vendor_uuid = $1`, uuid); err != nil {
		t.Fatalf("soft-delete vendor: %v", err)
	}
	return uuid
}

// seedVendorBill creates a live vendor_bill row directly (bypassing
// vendorbill.Create's line/tax machinery, which this header-only module's
// tests don't need) in the given status with the given grand_total, and
// returns its uuid. balance_due starts equal to grand_total since no
// settlement has been recorded against it yet.
func seedVendorBill(t *testing.T, pool *pgxpool.Pool, vendorUUID string, grandTotal float64, statusCode string) string {
	t.Helper()
	ctx := context.Background()
	var typeID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VBIL'`).Scan(&typeID); err != nil {
		t.Fatalf("resolve VBIL type: %v", err)
	}
	var statusID int
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, statusCode).Scan(&statusID); err != nil {
		t.Fatalf("resolve vendor bill %s status: %v", statusCode, err)
	}
	var vendorInternalID int
	if err := pool.QueryRow(ctx, `SELECT vendor_id FROM vendor WHERE vendor_uuid = $1`, vendorUUID).Scan(&vendorInternalID); err != nil {
		t.Fatalf("resolve vendor internal id: %v", err)
	}
	var billUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO vendor_bill (record_type, vendor_bill_status, vendor_bill_vendor_id, vendor_bill_vendor_name,
			vendor_bill_grand_total, vendor_bill_balance_due, vendor_bill_created_by)
		VALUES ($1, $2, $3, 'Test Vendor Bill Vendor', $4, $4, 1)
		RETURNING vendor_bill_uuid::text`, typeID, statusID, vendorInternalID, grandTotal).Scan(&billUUID); err != nil {
		t.Fatalf("seed vendor bill: %v", err)
	}
	return billUUID
}

// seedDeletedVendorBill seeds an otherwise-live (APPV) vendor bill and
// immediately soft-deletes it.
func seedDeletedVendorBill(t *testing.T, pool *pgxpool.Pool, vendorUUID string, grandTotal float64) string {
	t.Helper()
	ctx := context.Background()
	billUUID := seedVendorBill(t, pool, vendorUUID, grandTotal, "APPV")
	if _, err := pool.Exec(ctx,
		`UPDATE vendor_bill SET vendor_bill_deleted_at = NOW(), vendor_bill_deleted_by = 1 WHERE vendor_bill_uuid = $1`, billUUID); err != nil {
		t.Fatalf("soft-delete vendor bill: %v", err)
	}
	return billUUID
}

// seedApprovedCredit creates a vendor credit for vendorUUID at the given
// amount and transitions it DRFT->APPV, the precondition every Apply/Reverse
// test needs (appliableStatuses requires APPV or APPL).
func seedApprovedCredit(t *testing.T, pool *pgxpool.Pool, vendorUUID string, amount float64) *VendorCredit {
	t.Helper()
	ctx := context.Background()
	vc, err := Create(ctx, pool, CreateVendorCreditInput{
		VendorUUID: vendorUUID, Reason: "Test credit", Amount: amount,
	}, 1)
	if err != nil {
		t.Fatalf("seed vendor credit: %v", err)
	}
	vc, err = Transition(ctx, pool, vc.ID, "APPV", 1)
	if err != nil {
		t.Fatalf("approve vendor credit: %v", err)
	}
	return vc
}

// vendorBillBalances loads a vendor bill's live AP rollup + status for
// assertions.
func vendorBillBalances(t *testing.T, pool *pgxpool.Pool, billUUID string) (grandTotal, amountPaid, creditTotal, balanceDue float64, statusCode string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT vb.vendor_bill_grand_total, vb.vendor_bill_amount_paid, vb.vendor_bill_credit_total, vb.vendor_bill_balance_due,
		       rs.record_status_code
		FROM vendor_bill vb JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_uuid = $1`, billUUID,
	).Scan(&grandTotal, &amountPaid, &creditTotal, &balanceDue, &statusCode); err != nil {
		t.Fatalf("load vendor bill balances: %v", err)
	}
	return
}

// Scenario 1: Create rejects an inactive vendor (AD-8) and, separately, a
// deleted vendor (pre-existing "Unknown vendor." path) -- each with its own
// distinct error message.
func TestCreate_InactiveVendorRejected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	t.Run("inactive vendor", func(t *testing.T) {
		vendorUUID := seedInactiveVendor(t, pool)
		_, err := Create(ctx, pool, CreateVendorCreditInput{VendorUUID: vendorUUID, Reason: "Return", Amount: 100}, 1)
		if !IsClientError(err) {
			t.Fatalf("create against inactive vendor: err = %v, want ClientError", err)
		}
		if !strings.Contains(err.Error(), "not active") {
			t.Errorf("message = %q, want it to contain \"not active\"", err.Error())
		}
	})

	t.Run("deleted vendor", func(t *testing.T) {
		vendorUUID := seedDeletedVendor(t, pool)
		_, err := Create(ctx, pool, CreateVendorCreditInput{VendorUUID: vendorUUID, Reason: "Return", Amount: 100}, 1)
		if !IsClientError(err) {
			t.Fatalf("create against deleted vendor: err = %v, want ClientError", err)
		}
		if err.Error() != "Unknown vendor." {
			t.Errorf("message = %q, want %q", err.Error(), "Unknown vendor.")
		}
	})
}

// Scenario 2: Apply rejects a target bill in DRFT, PAID, VOID, or
// soft-deleted -- none of those are in vendorbill.PayableStatuses (or, for
// soft-deleted, resolve at all).
func TestApply_BillStatusGating(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID := seedVendor(t, pool)
	credit := seedApprovedCredit(t, pool, vendorUUID, 100)

	cases := []struct {
		name string
		seed func() string
	}{
		{"DRFT bill", func() string { return seedVendorBill(t, pool, vendorUUID, 100, "DRFT") }},
		{"PAID bill", func() string { return seedVendorBill(t, pool, vendorUUID, 100, "PAID") }},
		{"VOID bill", func() string { return seedVendorBill(t, pool, vendorUUID, 100, "VOID") }},
		{"soft-deleted bill", func() string { return seedDeletedVendorBill(t, pool, vendorUUID, 100) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			billUUID := tc.seed()
			_, err := Apply(ctx, pool, credit.ID, billUUID, 50, 1)
			if err == nil {
				t.Fatalf("apply against %s succeeded, want rejection", tc.name)
			}
			// DRFT/PAID/VOID resolve in vendorbill.LockForUpdate fine and fail
			// vendorcredit's own PayableStatuses gate (vendorcredit.ClientError);
			// soft-deleted doesn't resolve there at all (vendorbill.ClientError).
			if !IsClientError(err) && !vendorbill.IsClientError(err) {
				t.Fatalf("apply against %s: err = %v, want a ClientError", tc.name, err)
			}
		})
	}
}

// Scenario 3 (AD-9 regression guard): a voided vendor_payment's application
// must not count against a bill's deductible balance. If it did, a
// full-grand_total credit apply immediately after voiding the payment would
// be wrongly capped and rejected. No new vendorcredit logic is exercised
// here -- this proves vendorbill.RecomputeBalance's existing live-row
// filtering plus vendor_payment's VOID cascade already guarantee it.
func TestApply_ExcludesVoidedVendorPaymentApplication(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID := seedVendor(t, pool)
	billUUID := seedVendorBill(t, pool, vendorUUID, 100, "APPV")

	var methodID int
	if err := pool.QueryRow(ctx, `SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'CASH'`).Scan(&methodID); err != nil {
		t.Fatalf("resolve CASH payment method: %v", err)
	}

	vp, err := vendorpayment.Create(ctx, pool, vendorpayment.CreateVendorPaymentInput{
		VendorUUID: vendorUUID, MethodID: methodID, Amount: 100,
	}, 1)
	if err != nil {
		t.Fatalf("seed vendor payment: %v", err)
	}
	if _, err := vendorpayment.Apply(ctx, pool, vp.ID, billUUID, 100, 1); err != nil {
		t.Fatalf("apply vendor payment: %v", err)
	}

	// Voiding the payment must cascade-reverse its application (vendor_payment
	// AD-8), so the bill's live balance returns to its full grand_total.
	if _, err := vendorpayment.Transition(ctx, pool, vp.ID, "VOID", 1); err != nil {
		t.Fatalf("void vendor payment: %v", err)
	}

	credit := seedApprovedCredit(t, pool, vendorUUID, 100)
	applied, err := Apply(ctx, pool, credit.ID, billUUID, 100, 1)
	if err != nil {
		t.Fatalf("apply full credit after voided payment: %v -- a voided payment's application must not count against the bill's deductible balance (AD-9)", err)
	}
	if applied.AppliedTotal != 100 || applied.UnappliedAmount != 0 {
		t.Errorf("credit rollup = applied %v / unapplied %v, want 100 / 0", applied.AppliedTotal, applied.UnappliedAmount)
	}
}

// Scenario 4: a random, never-issued uuid is architecturally indistinguishable
// from a foreign tenant's id (tenant/sso_config_test.go's "not found -
// cross-tenant" reasoning) -- Get/Apply/Reverse must all return ErrNotFound,
// never a 500, never partial data.
func TestGetApplyReverse_UnknownUUID(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	t.Run("not found - cross-tenant", func(t *testing.T) {
		const randomID = "00000000-0000-0000-0000-000000000000"
		if _, err := Get(ctx, pool, randomID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(unknown) = %v, want ErrNotFound", err)
		}
		if _, err := Apply(ctx, pool, randomID, randomID, 10, 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("Apply(unknown credit) = %v, want ErrNotFound", err)
		}
		if _, err := Reverse(ctx, pool, randomID, randomID, 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("Reverse(unknown credit) = %v, want ErrNotFound", err)
		}
	})
}

// Scenario 5: over-cap Apply is rejected, never clamped -- post-call state
// (credit rollup + bill balance) must be byte-for-byte unchanged.
func TestApply_OverCreditRejected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID := seedVendor(t, pool)
	billUUID := seedVendorBill(t, pool, vendorUUID, 50, "APPV")
	credit := seedApprovedCredit(t, pool, vendorUUID, 100)

	// Cap is min(unapplied=100, balanceDue=50) = 50; one cent above must reject.
	if _, err := Apply(ctx, pool, credit.ID, billUUID, 50.01, 1); !IsClientError(err) {
		t.Fatalf("over-cap apply: err = %v, want ClientError", err)
	}

	gotCredit, err := Get(ctx, pool, credit.ID)
	if err != nil {
		t.Fatalf("get credit: %v", err)
	}
	if gotCredit.AppliedTotal != 0 || gotCredit.UnappliedAmount != 100 {
		t.Errorf("credit rollup after rejected over-apply = %v/%v, want 0/100 (no partial write)",
			gotCredit.AppliedTotal, gotCredit.UnappliedAmount)
	}

	_, _, creditTotal, balance, _ := vendorBillBalances(t, pool, billUUID)
	if creditTotal != 0 || balance != 50 {
		t.Errorf("bill balance after rejected over-apply = credit %v / balance %v, want 0/50 (no partial write)", creditTotal, balance)
	}
}

// Scenario 6: two goroutines apply the full amount concurrently against the
// same credit/bill pair. FOR UPDATE lock contention makes goroutine ordering
// nondeterministic by design, so the assertion is the post-hoc invariant
// (never over-credit, never a negative balance) rather than which goroutine
// won -- though for a single full-amount race, exactly one succeeding is
// itself a deterministic consequence of that same locking + cap check.
func TestApply_Concurrent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID := seedVendor(t, pool)
	billUUID := seedVendorBill(t, pool, vendorUUID, 100, "APPV")
	credit := seedApprovedCredit(t, pool, vendorUUID, 100)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := Apply(ctx, pool, credit.ID, billUUID, 100, 1)
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, err := range errs {
		switch {
		case err == nil:
			successCount++
		case !IsClientError(err):
			t.Errorf("concurrent apply: unexpected non-client error %v", err)
		}
	}
	if successCount != 1 {
		t.Errorf("concurrent full-amount apply: successCount = %d, want exactly 1 (row locks + cap check must serialize the race)", successCount)
	}

	gotCredit, err := Get(ctx, pool, credit.ID)
	if err != nil {
		t.Fatalf("get credit: %v", err)
	}
	if gotCredit.AppliedTotal > gotCredit.GrandTotal {
		t.Errorf("credit AppliedTotal = %v, exceeds GrandTotal %v -- over-credit invariant violated", gotCredit.AppliedTotal, gotCredit.GrandTotal)
	}
	if gotCredit.UnappliedAmount < 0 {
		t.Errorf("credit UnappliedAmount = %v, must never go negative", gotCredit.UnappliedAmount)
	}

	_, _, _, balance, _ := vendorBillBalances(t, pool, billUUID)
	if balance < 0 {
		t.Errorf("bill balance_due = %v, must never go negative", balance)
	}
}

// Scenario 7: DRFT->APPV succeeds via Transition; a second APPV->APPV is
// rejected (not in the map from APPV).
func TestTransition_ApprovalFlow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID := seedVendor(t, pool)

	vc, err := Create(ctx, pool, CreateVendorCreditInput{VendorUUID: vendorUUID, Reason: "Test", Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	approved, err := Transition(ctx, pool, vc.ID, "APPV", 1)
	if err != nil {
		t.Fatalf("DRFT->APPV: %v", err)
	}
	if approved.StatusCode != "APPV" || approved.StatusName != "Approved" {
		t.Fatalf("status = %q/%q, want APPV/Approved", approved.StatusCode, approved.StatusName)
	}

	if _, err := Transition(ctx, pool, vc.ID, "APPV", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("second APPV->APPV: err = %v, want ErrInvalidTransition", err)
	}
}

// Scenario 8: Apply then Reverse restores both the credit and the bill to
// their pre-Apply state exactly.
func TestApplyReverse_RoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID := seedVendor(t, pool)
	billUUID := seedVendorBill(t, pool, vendorUUID, 100, "APPV")
	credit := seedApprovedCredit(t, pool, vendorUUID, 100)

	applied, err := Apply(ctx, pool, credit.ID, billUUID, 100, 1)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.StatusCode != "APPL" || applied.UnappliedAmount != 0 {
		t.Fatalf("post-apply credit = status %q / unapplied %v, want APPL / 0", applied.StatusCode, applied.UnappliedAmount)
	}
	_, _, creditTotal, balance, status := vendorBillBalances(t, pool, billUUID)
	if creditTotal != 100 || balance != 0 || status != "PAID" {
		t.Fatalf("post-apply bill = credit %v / balance %v / status %q, want 100 / 0 / PAID", creditTotal, balance, status)
	}

	reversed, err := Reverse(ctx, pool, credit.ID, billUUID, 1)
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if reversed.StatusCode != "APPV" || reversed.AppliedTotal != 0 || reversed.UnappliedAmount != 100 {
		t.Fatalf("post-reverse credit = status %q / applied %v / unapplied %v, want APPV / 0 / 100",
			reversed.StatusCode, reversed.AppliedTotal, reversed.UnappliedAmount)
	}
	if len(reversed.Applications) != 0 {
		t.Errorf("post-reverse Applications = %d, want 0", len(reversed.Applications))
	}
	_, _, creditTotal, balance, status = vendorBillBalances(t, pool, billUUID)
	if creditTotal != 0 || balance != 100 || status != "APPV" {
		t.Errorf("post-reverse bill = credit %v / balance %v / status %q, want 0 / 100 / APPV", creditTotal, balance, status)
	}
}

// Scenario 9: Cancel (Transition ..., "VOID") from DRFT is trivial; from APPV
// with a live application it cascades a reversal; APPL->VOID is rejected
// directly -- an exhausted credit must be Reversed first.
func TestTransition_Cancellation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID := seedVendor(t, pool)

	t.Run("VOID from DRFT is trivial", func(t *testing.T) {
		vc, err := Create(ctx, pool, CreateVendorCreditInput{VendorUUID: vendorUUID, Reason: "Test", Amount: 50}, 1)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		voided, err := Transition(ctx, pool, vc.ID, "VOID", 1)
		if err != nil {
			t.Fatalf("DRFT->VOID: %v", err)
		}
		if voided.StatusCode != "VOID" {
			t.Errorf("status = %q, want VOID", voided.StatusCode)
		}
	})

	t.Run("VOID from APPV cascades reversal", func(t *testing.T) {
		billUUID := seedVendorBill(t, pool, vendorUUID, 100, "APPV")
		credit := seedApprovedCredit(t, pool, vendorUUID, 100)
		if _, err := Apply(ctx, pool, credit.ID, billUUID, 60, 1); err != nil {
			t.Fatalf("apply: %v", err)
		}

		voided, err := Transition(ctx, pool, credit.ID, "VOID", 1)
		if err != nil {
			t.Fatalf("APPV->VOID with a live application: %v", err)
		}
		if voided.StatusCode != "VOID" || voided.AppliedTotal != 0 || voided.UnappliedAmount != 100 {
			t.Errorf("post-void credit = status %q / applied %v / unapplied %v, want VOID / 0 / 100",
				voided.StatusCode, voided.AppliedTotal, voided.UnappliedAmount)
		}
		if len(voided.Applications) != 0 {
			t.Errorf("post-void Applications = %d, want 0", len(voided.Applications))
		}
		_, _, creditTotal, balance, status := vendorBillBalances(t, pool, billUUID)
		if creditTotal != 0 || balance != 100 || status != "APPV" {
			t.Errorf("post-void cascade bill = credit %v / balance %v / status %q, want 0 / 100 / APPV", creditTotal, balance, status)
		}
	})

	t.Run("APPL to VOID rejected directly", func(t *testing.T) {
		billUUID := seedVendorBill(t, pool, vendorUUID, 30, "APPV")
		credit := seedApprovedCredit(t, pool, vendorUUID, 30)
		applied, err := Apply(ctx, pool, credit.ID, billUUID, 30, 1)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if applied.StatusCode != "APPL" {
			t.Fatalf("precondition: status = %q, want APPL", applied.StatusCode)
		}
		if _, err := Transition(ctx, pool, credit.ID, "VOID", 1); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("APPL->VOID: err = %v, want ErrInvalidTransition (must Reverse first)", err)
		}
	})
}

// Scenario 10: a failure mid-Apply (a syntactically valid but nonexistent
// billUUID, failing at vendorbill.LockForUpdate after the credit lock/status
// gate already succeeded) must roll back the whole transaction -- a prior,
// otherwise-valid application on the same credit must be completely
// untouched.
func TestApply_RollbackOnFailure(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID := seedVendor(t, pool)
	billA := seedVendorBill(t, pool, vendorUUID, 100, "APPV")
	credit := seedApprovedCredit(t, pool, vendorUUID, 200)

	if _, err := Apply(ctx, pool, credit.ID, billA, 50, 1); err != nil {
		t.Fatalf("prior valid apply: %v", err)
	}
	before, err := Get(ctx, pool, credit.ID)
	if err != nil {
		t.Fatalf("get credit before failed apply: %v", err)
	}

	const nonexistentBillUUID = "11111111-1111-1111-1111-111111111111"
	if _, err := Apply(ctx, pool, credit.ID, nonexistentBillUUID, 30, 1); !vendorbill.IsClientError(err) {
		t.Fatalf("apply against nonexistent bill: err = %v, want vendorbill.ClientError", err)
	}

	after, err := Get(ctx, pool, credit.ID)
	if err != nil {
		t.Fatalf("get credit after failed apply: %v", err)
	}
	if after.AppliedTotal != before.AppliedTotal || after.UnappliedAmount != before.UnappliedAmount {
		t.Errorf("credit rollup after failed apply = %v/%v, want unchanged %v/%v",
			after.AppliedTotal, after.UnappliedAmount, before.AppliedTotal, before.UnappliedAmount)
	}
	if len(after.Applications) != len(before.Applications) {
		t.Fatalf("Applications len after failed apply = %d, want unchanged %d", len(after.Applications), len(before.Applications))
	}
	for i := range before.Applications {
		if after.Applications[i].Amount != before.Applications[i].Amount ||
			after.Applications[i].VendorBillID != before.Applications[i].VendorBillID {
			t.Errorf("Application[%d] changed after failed apply: before %+v, after %+v", i, before.Applications[i], after.Applications[i])
		}
	}
}
