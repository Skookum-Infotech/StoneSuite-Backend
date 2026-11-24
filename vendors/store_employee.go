package vendors

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// EmployeeVendorType is the vendor_type of the vendor an employee gets, so an
// expense claim can be billed and paid to the employee like any other payee.
const EmployeeVendorType = "Employee"

// inactiveStatusCode is the vendor status an inactive employee's vendor holds.
const inactiveStatusCode = "INA_"

// EmployeeProfile is the slice of an employee record its vendor mirrors.
type EmployeeProfile struct {
	EmployeeID int
	FirstName  string
	LastName   string
	Email      string
	Phone      string
	JobTitle   string
	Department string
	Address    string
	// Active is whether the employee can currently be paid: the vendor is Active
	// while the employee is, Inactive otherwise.
	Active bool
}

// fullName is the display name an Employee vendor carries.
func (p EmployeeProfile) fullName() string {
	name := strings.Join(strings.Fields(p.FirstName+" "+p.LastName), " ")
	if name == "" {
		name = strings.TrimSpace(p.Email)
	}
	return name
}

// fields maps the profile onto the vendor's columns. The full name is also
// stored as the legal name: the vendor list and picker read a non-Person
// vendor's name from vendor_legal_name.
func (p EmployeeProfile) fields() vendorFields {
	given := strings.TrimSpace(p.FirstName)
	if given == "" {
		given = p.fullName()
	}
	return vendorFields{
		Email:           strings.TrimSpace(p.Email),
		PhysicalAddress: p.Address,
		GivenName:       given,
		FamilyName:      strings.TrimSpace(p.LastName),
		JobTitle:        p.JobTitle,
		Department:      p.Department,
		LegalName:       p.fullName(),
		ContactPoint:    &ContactPoint{Telephone: p.Phone, Email: strings.TrimSpace(p.Email)},
	}
}

// EnsureForEmployeeTx makes sure the employee has one live Employee vendor that
// matches its profile and active state, and returns that vendor's uuid. It
// creates the vendor on first use, and otherwise brings the existing one up to
// date, so it is safe to call whenever the employee changes. It runs inside the
// caller's transaction (no Begin/Commit) and takes the vendor row's lock.
func EnsureForEmployeeTx(ctx context.Context, tx pgx.Tx, p EmployeeProfile, actorEmployeeID int) (string, error) {
	if p.EmployeeID <= 0 {
		return "", ClientError{Msg: "An employee is required."}
	}
	recordTypeID, err := recordTypeIDByCode(ctx, tx, vendorRecordTypeCode)
	if err != nil {
		return "", fmt.Errorf("resolve VNDR record type: %w", err)
	}
	wantCode := defaultStatusCode
	if !p.Active {
		wantCode = inactiveStatusCode
	}
	wantStatusID, err := statusIDByCode(ctx, tx, recordTypeID, wantCode)
	if err != nil {
		return "", fmt.Errorf("resolve %s vendor status: %w", wantCode, err)
	}

	var vendorID, curStatusID int
	var vendorUUID string
	err = tx.QueryRow(ctx, `
		SELECT vendor_id, vendor_uuid, vendor_status
		FROM vendor
		WHERE vendor_employee_id = $1 AND vendor_deleted_at IS NULL
		FOR UPDATE`, p.EmployeeID).Scan(&vendorID, &vendorUUID, &curStatusID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return createForEmployeeTx(ctx, tx, p, recordTypeID, wantStatusID, actorEmployeeID)
	case err != nil:
		return "", fmt.Errorf("load employee vendor: %w", err)
	}

	cv := append(vendorColVals(p.fields()),
		colVal{"vendor_status", wantStatusID, ""},
		colVal{"vendor_updated_by", nullableInt(actorEmployeeID), ""},
	)
	updateSQL, updateArgs := buildUpdateSet("vendor", []any{vendorID}, cv,
		[]string{"vendor_updated_at = NOW()", "vendor_record_version = vendor_record_version + 1"},
		"vendor_id = $1")
	if _, err := tx.Exec(ctx, updateSQL, updateArgs...); err != nil {
		return "", fmt.Errorf("sync employee vendor: %w", err)
	}
	if curStatusID != wantStatusID {
		writeHistory(ctx, tx, vendorID, "transition", &curStatusID, &wantStatusID, actorEmployeeID)
	} else {
		writeHistory(ctx, tx, vendorID, "update", nil, nil, actorEmployeeID)
	}
	return vendorUUID, nil
}

// createForEmployeeTx inserts the employee's vendor. The duplicate-name guard is
// skipped on purpose: two employees may share a name, and the vendor is tied to
// its employee by vendor_employee_id, not by name.
func createForEmployeeTx(ctx context.Context, tx pgx.Tx, p EmployeeProfile, recordTypeID, statusID, actorEmployeeID int) (string, error) {
	cv := append([]colVal{
		{"record_type", recordTypeID, ""},
		{"vendor_status", statusID, ""},
		{"vendor_type", EmployeeVendorType, ""},
		{"vendor_owner_id", nullableInt(actorEmployeeID), ""},
		{"vendor_employee_id", p.EmployeeID, ""},
	}, vendorColVals(p.fields())...)
	cv = append(cv, colVal{"vendor_created_by", nullableInt(actorEmployeeID), ""})

	insertSQL, insertArgs := buildInsert("vendor", cv, "vendor_id, vendor_uuid")
	var internalID int
	var newUUID string
	if err := tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID); err != nil {
		return "", fmt.Errorf("insert employee vendor: %w", err)
	}
	if _, err := assignNumber(ctx, tx, int64(internalID)); err != nil {
		return "", err
	}
	writeHistory(ctx, tx, internalID, "create", nil, &statusID, actorEmployeeID)
	return newUUID, nil
}
