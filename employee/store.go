package employee

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/historywrite"
	"stonesuite-backend/vendors"
	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when an employee uuid matches nothing live.
var ErrNotFound = errors.New("employee not found")

// ErrVersionConflict is returned when an update carries an expectedVersion that
// no longer matches the stored record: someone else saved first.
var ErrVersionConflict = errors.New("this employee was changed by someone else; reload it and try again")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// recordTypeIDByCode resolves a lkp_record_type code to its internal id.
func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

// statusIDByCode resolves a lkp_record_status code (scoped to a record type) to
// its internal id.
func statusIDByCode(ctx context.Context, q workflow.Querier, recordTypeID int, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = $2`, recordTypeID, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("status %q: %w", code, err)
	}
	return id, nil
}

// writeHistory records one employee_history row inside the caller's transaction.
func writeHistory(ctx context.Context, tx pgx.Tx, employeeID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	historywrite.Exec(ctx, tx, `
		INSERT INTO employee_history (employee_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		employeeID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// ----- header select + scan -------------------------------------------------

// empSelect is the base SELECT shared by Get and Search. Column order must match
// scanEmployee exactly; alias `e` is the employee table. A row written before
// the module existed has no status yet, so the label/code fall back to what
// employee_is_active says. The system employee is never listed.
const empSelect = `
	SELECT e.employee_uuid, COALESCE(e.employee_number,''),
	       COALESCE(rs.record_status_name, CASE WHEN e.employee_is_active THEN 'Active' ELSE 'Inactive' END),
	       COALESCE(rs.record_status_code, CASE WHEN e.employee_is_active THEN 'ACTV' ELSE 'INAC' END),
	       e.employee_approval_status,
	       e.employee_first_name, e.employee_last_name, e.employee_email, e.employee_phone,
	       e.employee_job_title, e.employee_department, e.employee_address,
	       e.employee_user_id IS NOT NULL, e.employee_is_active,
	       COALESCE(v.vendor_uuid::text,''), COALESCE(v.vendor_number,''),
	       e.employee_record_version, e.employee_created_at, e.employee_updated_at,
	       COALESCE(e.employee_status, 0)
	FROM employee e
	LEFT JOIN lkp_record_status rs ON rs.record_status_id = e.employee_status
	LEFT JOIN vendor v ON v.vendor_employee_id = e.employee_id AND v.vendor_deleted_at IS NULL`

// empLive is the predicate every read carries: not deleted, not the system row.
const empLive = `e.employee_deleted_at IS NULL AND NOT e.employee_is_system`

// empMeta carries the numeric status id the response does not expose; Search
// needs it to mint a keyset cursor for the `status` sort.
type empMeta struct{ statusID int }

func scanEmployee(row pgx.Row) (*Employee, empMeta, error) {
	var e Employee
	var meta empMeta
	if err := row.Scan(
		&e.ID, &e.Number, &e.Status, &e.StatusCode, &e.ApprovalStatus,
		&e.FirstName, &e.LastName, &e.Email, &e.Phone,
		&e.JobTitle, &e.Department, &e.Address,
		&e.HasLogin, &e.IsActive,
		&e.VendorID, &e.VendorNumber,
		&e.RecordVersion, &e.CreatedAt, &e.UpdatedAt,
		&meta.statusID,
	); err != nil {
		return nil, empMeta{}, err
	}
	e.DisplayName = displayName(e.FirstName, e.LastName, e.Email)
	e.NextStatusCodes = NextStatuses(e.StatusCode)
	return &e, meta, nil
}

// displayName is the person's name for lists and pickers, falling back to the
// email when no name was entered.
func displayName(first, last, email string) string {
	name := strings.Join(strings.Fields(first+" "+last), " ")
	if name == "" {
		return email
	}
	return name
}

// Get loads one live employee by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Employee, error) {
	return getQ(ctx, pool, uuid)
}

func getQ(ctx context.Context, q workflow.Querier, uuid string) (*Employee, error) {
	e, _, err := scanEmployee(q.QueryRow(ctx, empSelect+` WHERE e.employee_uuid = $1 AND `+empLive, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get employee: %w", err)
	}
	return e, nil
}

// ----- vendor sync ----------------------------------------------------------

// syncVendorTx brings the employee's Vendor record in line with the employee,
// inside the caller's transaction. An Active employee always has a vendor
// (created here the first time); an Inactive one keeps its existing vendor, now
// Inactive; a Pending one has none yet.
func syncVendorTx(ctx context.Context, tx pgx.Tx, employeeID, actorEmployeeID int) error {
	var p vendors.EmployeeProfile
	var statusCode string
	err := tx.QueryRow(ctx, `
		SELECT e.employee_id, e.employee_first_name, e.employee_last_name, e.employee_email,
		       e.employee_phone, e.employee_job_title, e.employee_department, e.employee_address,
		       COALESCE(rs.record_status_code, CASE WHEN e.employee_is_active THEN 'ACTV' ELSE 'INAC' END)
		FROM employee e
		LEFT JOIN lkp_record_status rs ON rs.record_status_id = e.employee_status
		WHERE e.employee_id = $1`, employeeID,
	).Scan(&p.EmployeeID, &p.FirstName, &p.LastName, &p.Email,
		&p.Phone, &p.JobTitle, &p.Department, &p.Address, &statusCode)
	if err != nil {
		return fmt.Errorf("load employee for vendor sync: %w", err)
	}
	switch statusCode {
	case statusActive:
		p.Active = true
	case statusInactive:
		var has bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM vendor WHERE vendor_employee_id = $1 AND vendor_deleted_at IS NULL)`,
			employeeID).Scan(&has); err != nil {
			return fmt.Errorf("check employee vendor: %w", err)
		}
		if !has {
			return nil
		}
	default:
		return nil
	}
	if _, err := vendors.EnsureForEmployeeTx(ctx, tx, p, actorEmployeeID); err != nil {
		return fmt.Errorf("sync employee vendor: %w", err)
	}
	return nil
}

// EnsureVendorTx makes sure the employee has an up-to-date Employee vendor and
// returns its uuid. It is for callers that need to bill an employee (an approved
// expense claim) and may meet one whose vendor was never created, such as a
// login user who predates this module. Run it inside the caller's transaction.
func EnsureVendorTx(ctx context.Context, tx pgx.Tx, employeeID, actorEmployeeID int) (string, error) {
	var p vendors.EmployeeProfile
	err := tx.QueryRow(ctx, `
		SELECT employee_id, employee_first_name, employee_last_name, employee_email,
		       employee_phone, employee_job_title, employee_department, employee_address
		FROM employee WHERE employee_id = $1 AND employee_deleted_at IS NULL`, employeeID,
	).Scan(&p.EmployeeID, &p.FirstName, &p.LastName, &p.Email,
		&p.Phone, &p.JobTitle, &p.Department, &p.Address)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ClientError{Msg: "The claimant's employee record was not found."}
	}
	if err != nil {
		return "", fmt.Errorf("load employee for vendor: %w", err)
	}
	p.Active = true
	return vendors.EnsureForEmployeeTx(ctx, tx, p, actorEmployeeID)
}

// ----- approval notification ------------------------------------------------

// notifyPending best-effort-notifies the configured approvers that a new
// employee is waiting for sign-off.
func notifyPending(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, recordTypeID, pendingStatusID, actorEmployeeID int) {
	cfg := moduleConfig()
	rec := cfg.Record
	approvalchain.NotifyApprovalRequested(ctx, pool, approvalchain.EventContext{
		Table: rec.Table, IDColumn: rec.IDColumn, NumberColumn: rec.NumberColumn,
		ApproverTable: cfg.ApproverTable, RecordTypeID: recordTypeID, StatusID: pendingStatusID,
		InternalID: internalID, ActorEmployeeID: actorEmployeeID,
		Resource: cfg.Resource, DisplayName: cfg.DisplayName, RecordUUID: uuid,
	})
}
