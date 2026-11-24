//go:build dbtest

package employee

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
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

func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@test.local", prefix, time.Now().UnixNano())
}

// With no approver configured a new employee is Active at once, numbered, and
// gets an Employee vendor.
func TestCreate_UngatedIsActiveWithVendor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	emp, err := Create(ctx, pool, CreateEmployeeInput{employeeFields: employeeFields{
		FirstName: "Ana", LastName: "Diaz", Email: uniqueEmail("ana"), Department: "Field",
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if emp.StatusCode != "ACTV" || !emp.IsActive {
		t.Errorf("status = %q active=%v, want ACTV/true", emp.StatusCode, emp.IsActive)
	}
	if emp.Number == "" {
		t.Error("employee has no number")
	}
	if emp.VendorID == "" || emp.VendorNumber == "" {
		t.Fatalf("Active employee has no vendor: %+v", emp)
	}
	var vtype, name string
	if err := pool.QueryRow(ctx, `SELECT vendor_type, vendor_legal_name FROM vendor WHERE vendor_uuid = $1`, emp.VendorID).Scan(&vtype, &name); err != nil {
		t.Fatalf("load vendor: %v", err)
	}
	if vtype != "Employee" || name != "Ana Diaz" {
		t.Errorf("vendor = %q/%q, want Employee/Ana Diaz", vtype, name)
	}

	// An edit carries over to the vendor; deactivating makes it Inactive.
	if _, err := Update(ctx, pool, emp.ID, UpdateEmployeeInput{employeeFields: employeeFields{
		FirstName: "Ana", LastName: "Diaz-Lopez", Email: emp.Email,
	}}, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT vendor_legal_name FROM vendor WHERE vendor_uuid = $1`, emp.VendorID).Scan(&name); err != nil || name != "Ana Diaz-Lopez" {
		t.Errorf("vendor name after edit = %q (%v), want Ana Diaz-Lopez", name, err)
	}
	off, err := Transition(ctx, pool, emp.ID, "INAC", 1)
	if err != nil {
		t.Fatalf("Transition to INAC: %v", err)
	}
	if off.IsActive {
		t.Error("deactivated employee still reads active")
	}
	var vstatus string
	if err := pool.QueryRow(ctx, `
		SELECT rs.record_status_code FROM vendor v JOIN lkp_record_status rs ON rs.record_status_id = v.vendor_status
		WHERE v.vendor_uuid = $1`, emp.VendorID).Scan(&vstatus); err != nil || vstatus != "INA_" {
		t.Errorf("vendor status = %q (%v), want INA_", vstatus, err)
	}
}

func TestCreate_DuplicateEmailIsRejected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	email := uniqueEmail("dup")
	in := CreateEmployeeInput{employeeFields: employeeFields{FirstName: "Dee", Email: email}}
	if _, err := Create(ctx, pool, in, 1); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	in.Email = "  " + email + "  "
	_, err := Create(ctx, pool, in, 1)
	var ce ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("second Create = %v, want a ClientError", err)
	}
}

// With an approver configured the employee waits: no vendor, not usable. The
// sign-off (here a super admin's override) activates it and creates the vendor in the same step.
func TestApproval_ActivatesEmployeeAndCreatesVendor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	recordTypeID, err := recordTypeIDByCode(ctx, pool, recordTypeCode)
	if err != nil {
		t.Fatal(err)
	}
	pendingID, err := statusIDByCode(ctx, pool, recordTypeID, statusPending)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO employee_approver (record_type_id, record_status_id, approver_employee_id) VALUES ($1,$2,1)
		ON CONFLICT DO NOTHING`, recordTypeID, pendingID); err != nil {
		t.Fatalf("seed approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM employee_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, pendingID)
	})

	emp, err := Create(ctx, pool, CreateEmployeeInput{employeeFields: employeeFields{FirstName: "Pat", Email: uniqueEmail("pat")}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if emp.StatusCode != "PEND" || emp.IsActive || emp.VendorID != "" {
		t.Fatalf("gated employee = %+v, want pending, inactive, no vendor", emp)
	}
	if _, err := Transition(ctx, pool, emp.ID, "ACTV", 1); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("manual activate before approval = %v, want ErrApprovalRequired", err)
	}

	// The seeded approver is the system employee, which has no login, so the gate is
	// orphaned and only a super admin's override can clear it -- the same hook runs.
	done, err := Approve(ctx, pool, emp.ID, 1, true)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if done.StatusCode != "ACTV" || !done.IsActive || done.VendorID == "" {
		t.Errorf("approved employee = %+v, want active with a vendor", done)
	}
}

// The list page's every sort option must work end to end.
func TestSearch_EverySortFieldWorks(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := Create(ctx, pool, CreateEmployeeInput{employeeFields: employeeFields{FirstName: "Sorty", Email: uniqueEmail("sort")}}, 1); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for key := range sortFields {
		for _, dir := range []query.Direction{query.DirAsc, query.DirDesc} {
			page, err := Search(ctx, pool, query.Request{Sort: []query.SortKey{{Field: key, Dir: dir}}, Limit: 1})
			if err != nil {
				t.Fatalf("Search sorted by %s %s: %v", key, dir, err)
			}
			if len(page.Records) == 0 {
				t.Errorf("Search sorted by %s %s returned nothing", key, dir)
			}
		}
	}
}
