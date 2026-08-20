//go:build dbtest

package approvalchain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

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

// seedEmployee inserts one active employee for use as an approver and
// returns its id as both an int and the string form the API deals in. The
// email is suffixed with a nanosecond timestamp (mirrors
// estimate/store_test.go's seedCustomerAndItem) so repeat runs against a
// persistent test database never collide on employee's unique email
// constraint.
func seedEmployee(t *testing.T, pool *pgxpool.Pool) (id int, idStr string) {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO employee (employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ('Test', 'Approver', $1, 1) RETURNING employee_id`,
		"approver-"+suffix+"@example.test").Scan(&id); err != nil {
		t.Fatalf("seed employee: %v", err)
	}
	return id, fmt.Sprint(id)
}

func TestReplaceApprovers_And_GatesWithApprovers(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cfg, ok := ForWorkflowKey("estimate")
	if !ok {
		t.Fatal("estimate must be registered")
	}
	_, empIDStr := seedEmployee(t, pool)

	// Assign the seeded employee as the PAPV gate's approver.
	got, err := ReplaceApprovers(ctx, pool, cfg, "PAPV", []string{empIDStr}, 0)
	if err != nil {
		t.Fatalf("ReplaceApprovers: %v", err)
	}
	if len(got) != 1 || got[0] != empIDStr {
		t.Fatalf("ReplaceApprovers returned %v, want [%s]", got, empIDStr)
	}

	gates, err := GatesWithApprovers(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("GatesWithApprovers: %v", err)
	}
	if len(gates) != 1 {
		t.Fatalf("len(gates) = %d, want 1", len(gates))
	}
	if gates[0].StatusCode != "PAPV" {
		t.Errorf("gates[0].StatusCode = %q, want PAPV", gates[0].StatusCode)
	}
	if gates[0].StatusLabel == "" {
		t.Error("gates[0].StatusLabel must not be empty")
	}
	if len(gates[0].ApproverEmployeeIDs) != 1 || gates[0].ApproverEmployeeIDs[0] != empIDStr {
		t.Errorf("gates[0].ApproverEmployeeIDs = %v, want [%s]", gates[0].ApproverEmployeeIDs, empIDStr)
	}

	// Replacing with an empty list clears the gate.
	got, err = ReplaceApprovers(ctx, pool, cfg, "PAPV", []string{}, 0)
	if err != nil {
		t.Fatalf("ReplaceApprovers (clear): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReplaceApprovers (clear) returned %v, want empty", got)
	}
}

func TestReplaceApprovers_UnknownEmployee(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cfg, _ := ForWorkflowKey("estimate")

	_, err := ReplaceApprovers(ctx, pool, cfg, "PAPV", []string{"999999999"}, 0)
	if !errors.Is(err, ErrUnknownApprover) {
		t.Fatalf("ReplaceApprovers with bogus employee id: got %v, want ErrUnknownApprover", err)
	}
}

// TestReplaceApprovers_DuplicateEmployeeID guards against the INSERT loop
// iterating over the raw (non-deduped) employeeIDs while validation checks
// the deduped set -- a duplicate id would pass validation but then hit the
// UNIQUE(record_type_id, record_status_id, approver_employee_id) constraint
// on the second insert, so the whole call must instead succeed with exactly
// one approver.
func TestReplaceApprovers_DuplicateEmployeeID(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cfg, ok := ForWorkflowKey("estimate")
	if !ok {
		t.Fatal("estimate must be registered")
	}
	_, empIDStr := seedEmployee(t, pool)

	got, err := ReplaceApprovers(ctx, pool, cfg, "PAPV", []string{empIDStr, empIDStr}, 0)
	if err != nil {
		t.Fatalf("ReplaceApprovers with duplicate employee id: %v", err)
	}
	if len(got) != 1 || got[0] != empIDStr {
		t.Fatalf("ReplaceApprovers returned %v, want [%s]", got, empIDStr)
	}

	gates, err := GatesWithApprovers(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("GatesWithApprovers: %v", err)
	}
	if len(gates) != 1 {
		t.Fatalf("len(gates) = %d, want 1", len(gates))
	}
	if len(gates[0].ApproverEmployeeIDs) != 1 || gates[0].ApproverEmployeeIDs[0] != empIDStr {
		t.Errorf("gates[0].ApproverEmployeeIDs = %v, want [%s]", gates[0].ApproverEmployeeIDs, empIDStr)
	}
}
