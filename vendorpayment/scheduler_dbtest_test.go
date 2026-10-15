//go:build dbtest

package vendorpayment

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// scheduledVendorPayment creates a vendor payment with the given scheduled
// date and moves it to SCHD (no approvers configured, so DRFT->SCHD passes
// straight through the approval checkpoint).
func scheduledVendorPayment(t *testing.T, pool *pgxpool.Pool, scheduled time.Time) string {
	t.Helper()
	ctx := context.Background()
	var methodID int
	if err := pool.QueryRow(ctx, `
		SELECT payment_method_id FROM lkp_payment_method
		WHERE payment_method_deleted_at IS NULL AND payment_method_is_active ORDER BY payment_method_id LIMIT 1`).Scan(&methodID); err != nil {
		t.Fatalf("resolve an active payment method: %v", err)
	}
	created, err := Create(ctx, pool, CreateVendorPaymentInput{
		VendorUUID: seedVendor(t, pool), MethodID: methodID, Amount: 100, ScheduledDate: &scheduled,
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "SCHD", 1); err != nil {
		t.Fatalf("Transition to SCHD: %v", err)
	}
	return created.ID
}

func statusCodeOf(t *testing.T, pool *pgxpool.Pool, uuid string) string {
	t.Helper()
	var code string
	if err := pool.QueryRow(context.Background(), `
		SELECT rs.record_status_code FROM vendor_payment vp
		JOIN lkp_record_status rs ON rs.record_status_id = vp.vendor_payment_status
		WHERE vp.vendor_payment_uuid = $1`, uuid).Scan(&code); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return code
}

// TestRunSchedulerTick_DispatchesOnlyPaymentsDueByTenantToday covers the
// tenant-local date cut-off: a payment dated "today" (tenant calendar) and
// one dated yesterday go out; tomorrow's stays SCHD; a second tick finds
// nothing left to do; the SENT history row carries the system actor.
func TestRunSchedulerTick_DispatchesOnlyPaymentsDueByTenantToday(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	today := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)

	yesterday := scheduledVendorPayment(t, pool, today.AddDate(0, 0, -1))
	dueToday := scheduledVendorPayment(t, pool, today)
	tomorrow := scheduledVendorPayment(t, pool, today.AddDate(0, 0, 1))

	res, err := RunSchedulerTick(ctx, pool, today)
	if err != nil {
		t.Fatalf("RunSchedulerTick: %v", err)
	}
	if res.Transitioned < 2 {
		t.Errorf("transitioned = %d, want at least the 2 payments seeded here", res.Transitioned)
	}
	for uuid, want := range map[string]string{yesterday: "SENT", dueToday: "SENT", tomorrow: "SCHD"} {
		if got := statusCodeOf(t, pool, uuid); got != want {
			t.Errorf("payment %s status = %q, want %q", uuid, got, want)
		}
	}

	var actor int
	if err := pool.QueryRow(ctx, `
		SELECT h.actor_employee_id FROM vendor_payment_history h
		JOIN vendor_payment vp ON vp.vendor_payment_id = h.vendor_payment_id
		JOIN lkp_record_status rs ON rs.record_status_id = h.to_status_id
		WHERE vp.vendor_payment_uuid = $1 AND rs.record_status_code = 'SENT'`, dueToday).Scan(&actor); err != nil {
		t.Fatalf("read SENT history row: %v", err)
	}
	if actor != systemEmployeeID {
		t.Errorf("SENT history actor = %d, want system actor %d", actor, systemEmployeeID)
	}

	again, err := RunSchedulerTick(ctx, pool, today)
	if err != nil {
		t.Fatalf("second RunSchedulerTick: %v", err)
	}
	if got := statusCodeOf(t, pool, tomorrow); got != "SCHD" {
		t.Errorf("tomorrow's payment status after second tick = %q, want SCHD", got)
	}
	if again.Transitioned != 0 {
		t.Errorf("second tick transitioned = %d, want 0 (nothing newly due)", again.Transitioned)
	}
}
