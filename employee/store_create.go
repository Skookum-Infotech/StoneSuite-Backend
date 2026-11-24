package employee

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/idempotency"
)

// idempotencyScopeCreate namespaces employee creates in request_idempotency.
const idempotencyScopeCreate = "employee.create"

// Column limits mirror the employee table.
const (
	maxNameLen    = 100
	maxEmailLen   = 255
	maxPhoneLen   = 30
	maxTitleLen   = 100
	maxAddressLen = 2000
)

// normalize trims every field of the profile.
func (f employeeFields) normalize() employeeFields {
	f.FirstName = strings.TrimSpace(f.FirstName)
	f.LastName = strings.TrimSpace(f.LastName)
	f.Email = strings.TrimSpace(f.Email)
	f.Phone = strings.TrimSpace(f.Phone)
	f.JobTitle = strings.TrimSpace(f.JobTitle)
	f.Department = strings.TrimSpace(f.Department)
	f.Address = strings.TrimSpace(f.Address)
	return f
}

// validate checks the (already normalized) profile.
func (f employeeFields) validate() error {
	if f.FirstName == "" {
		return ClientError{Msg: "First name is required."}
	}
	if f.Email == "" {
		return ClientError{Msg: "Email is required."}
	}
	if _, err := mail.ParseAddress(f.Email); err != nil || strings.ContainsAny(f.Email, " <>") {
		return ClientError{Msg: "Enter a valid email address."}
	}
	limits := []struct {
		label string
		value string
		max   int
	}{
		{"First name", f.FirstName, maxNameLen}, {"Last name", f.LastName, maxNameLen},
		{"Email", f.Email, maxEmailLen}, {"Phone", f.Phone, maxPhoneLen},
		{"Job title", f.JobTitle, maxTitleLen}, {"Department", f.Department, maxTitleLen},
		{"Address", f.Address, maxAddressLen},
	}
	for _, l := range limits {
		if utf8.RuneCountInString(l.value) > l.max {
			return ClientError{Msg: fmt.Sprintf("%s must be at most %d characters.", l.label, l.max)}
		}
	}
	return nil
}

// emailTaken reports whether another live employee already uses the email
// (case-insensitively). excludeID skips the employee being edited (0 on create).
func emailTaken(ctx context.Context, tx pgx.Tx, email string, excludeID int) (bool, error) {
	var taken bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM employee
		               WHERE LOWER(employee_email) = LOWER($1) AND employee_id <> $2)`,
		email, excludeID).Scan(&taken)
	if err != nil {
		return false, fmt.Errorf("check employee email: %w", err)
	}
	return taken, nil
}

const errEmailTaken = "An employee with this email already exists."

// Create inserts a new employee. With approvers configured for Pending Approval
// it starts there (not yet usable anywhere); otherwise it is Active at once and
// gets its Vendor record in the same transaction.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateEmployeeInput, actorEmployeeID int) (*Employee, error) {
	f := in.normalize()
	if err := f.validate(); err != nil {
		return nil, err
	}

	// A retry whose first attempt already committed gets that employee back.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Lookup(ctx, pool, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			return Get(ctx, pool, prior)
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create employee: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if in.IdempotencyKey != "" {
		prior, err := idempotency.Claim(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			_ = tx.Rollback(ctx)
			return Get(ctx, pool, prior)
		}
	}

	if taken, err := emailTaken(ctx, tx, f.Email, 0); err != nil {
		return nil, err
	} else if taken {
		return nil, ClientError{Msg: errEmailTaken}
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve EMPL record type: %w", err)
	}
	pendingID, err := statusIDByCode(ctx, tx, recordTypeID, statusPending)
	if err != nil {
		return nil, err
	}
	activeID, err := statusIDByCode(ctx, tx, recordTypeID, statusActive)
	if err != nil {
		return nil, err
	}
	required, err := approvalchain.ActiveApproverCount(ctx, tx, moduleConfig().ApproverTable, recordTypeID, pendingID)
	if err != nil {
		return nil, err
	}
	gated := required > 0
	statusID, approvalStatus := activeID, approvalchain.StatusNone
	if gated {
		statusID, approvalStatus = pendingID, approvalchain.StatusPending
	}

	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO employee (
			employee_first_name, employee_last_name, employee_email, employee_phone,
			employee_job_title, employee_department, employee_address,
			employee_status, employee_approval_status, employee_is_active,
			employee_created_by, employee_updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		RETURNING employee_id, employee_uuid`,
		f.FirstName, f.LastName, f.Email, f.Phone, f.JobTitle, f.Department, f.Address,
		statusID, approvalStatus, !gated, nullableInt(actorEmployeeID),
	).Scan(&internalID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert employee: %w", err)
	}

	if _, err := assignNumber(ctx, tx, int64(internalID)); err != nil {
		return nil, err
	}
	if in.IdempotencyKey != "" {
		if err := idempotency.Complete(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, newUUID); err != nil {
			return nil, err
		}
	}
	writeHistory(ctx, tx, internalID, "create", nil, &statusID, actorEmployeeID)

	if !gated {
		if err := syncVendorTx(ctx, tx, internalID, actorEmployeeID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create employee: %w", err)
	}
	if gated {
		notifyPending(ctx, pool, newUUID, internalID, recordTypeID, pendingID, actorEmployeeID)
	}
	return Get(ctx, pool, newUUID)
}

// Update replaces a live employee's profile. Editing a rejected pending employee
// reopens its approval; editing an Active or Inactive one carries the change
// over to its Vendor record. The email of an employee that is a login user is
// its sign-in identity and cannot be changed here.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateEmployeeInput, actorEmployeeID int) (*Employee, error) {
	f := in.normalize()
	if err := f.validate(); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update employee: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, version int
	var curEmail string
	var hasLogin bool
	err = tx.QueryRow(ctx, `
		SELECT e.employee_id, e.employee_record_version, e.employee_email, e.employee_user_id IS NOT NULL
		FROM employee e
		WHERE e.employee_uuid = $1 AND `+empLive+`
		FOR UPDATE OF e`, uuid,
	).Scan(&internalID, &version, &curEmail, &hasLogin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load employee for update: %w", err)
	}
	if in.ExpectedVersion != nil && *in.ExpectedVersion != version {
		return nil, ErrVersionConflict
	}
	if hasLogin && !strings.EqualFold(f.Email, curEmail) {
		return nil, ClientError{Msg: "This employee can sign in, so their email can only be changed from Configuration > Users."}
	}
	if !strings.EqualFold(f.Email, curEmail) {
		if taken, err := emailTaken(ctx, tx, f.Email, internalID); err != nil {
			return nil, err
		} else if taken {
			return nil, ClientError{Msg: errEmailTaken}
		}
	}
	if hasLogin {
		f.Email = curEmail
	}

	if _, err := tx.Exec(ctx, `
		UPDATE employee SET
			employee_first_name = $2, employee_last_name = $3, employee_email = $4, employee_phone = $5,
			employee_job_title = $6, employee_department = $7, employee_address = $8,
			employee_updated_at = NOW(), employee_updated_by = $9,
			employee_record_version = employee_record_version + 1
		WHERE employee_id = $1`,
		internalID, f.FirstName, f.LastName, f.Email, f.Phone, f.JobTitle, f.Department, f.Address,
		nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("update employee: %w", err)
	}
	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	// A rejected pending employee that was just edited goes back to the approvers.
	resubmission, err := approvalchain.ResubmitAfterEdit(ctx, tx, moduleConfig(), internalID)
	if err != nil {
		return nil, err
	}
	if err := syncVendorTx(ctx, tx, internalID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update employee: %w", err)
	}
	resubmission.Notify(ctx, pool, uuid, actorEmployeeID)
	return Get(ctx, pool, uuid)
}
