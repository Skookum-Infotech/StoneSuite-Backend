// Package employee implements the Employee module (Configuration > Employee):
// the tenant's staff directory, layered on the long-standing employee table that
// every audit, owner and approver column already references. A new employee goes
// through an optional approval chain (Configure > Workflows > Employee), and an
// Active employee has a matching Vendor record (vendor_type "Employee") so
// expense claims can be billed and paid to them.
//
// An employee is not a login: employee_user_id links to a users row only for
// people who can sign in, and creating an employee here never creates a login.
package employee

import "time"

// employeeFields is the profile payload shared by create and update.
type employeeFields struct {
	FirstName  string `json:"firstName"`
	LastName   string `json:"lastName"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	JobTitle   string `json:"jobTitle"`
	Department string `json:"department"`
	Address    string `json:"address"`
}

// CreateEmployeeInput is the create-request payload.
type CreateEmployeeInput struct {
	employeeFields
	// IdempotencyKey is the caller-namespaced Idempotency-Key header, set by the
	// controller; empty means no replay protection. Never read from the body.
	IdempotencyKey string `json:"-"`
	// RequestHash fingerprints the request body for key-reuse detection.
	RequestHash string `json:"-"`
}

// UpdateEmployeeInput is the update-request payload.
type UpdateEmployeeInput struct {
	employeeFields
	// ExpectedVersion, when set, must match the stored record version or the
	// update fails with ErrVersionConflict.
	ExpectedVersion *int `json:"expectedVersion,omitempty"`
}

// Employee is the API response for one employee.
type Employee struct {
	ID             string `json:"id"`
	Number         string `json:"employeeNumber"`
	Status         string `json:"status"`         // human label, e.g. "Active"
	StatusCode     string `json:"statusCode"`     // lkp_record_status code: PEND | ACTV | INAC
	ApprovalStatus string `json:"approvalStatus"` // none | pending | approved | rejected
	// NextStatusCodes are the statuses a manual transition may move the employee
	// to right now. Moving a pending employee on to Active is offered only while
	// nobody is configured to approve it.
	NextStatusCodes []string `json:"nextStatusCodes"`

	employeeFields
	DisplayName string `json:"displayName"`
	// HasLogin is true when the employee is a login user (employee_user_id set).
	// Such an employee's email is its sign-in identity and cannot be edited here.
	HasLogin bool `json:"hasLogin"`
	IsActive bool `json:"isActive"`
	// VendorID / VendorNumber identify the employee's Vendor record, empty until
	// the employee is first Active.
	VendorID     string `json:"vendorId,omitempty"`
	VendorNumber string `json:"vendorNumber,omitempty"`

	RecordVersion int       `json:"recordVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Page is one page of a keyset-paginated employee search.
type Page struct {
	Records    []Employee
	NextCursor string
	HasMore    bool
}

// HistoryEntry is one row of an employee's status trail.
type HistoryEntry struct {
	Action     string    `json:"action"`
	FromStatus string    `json:"fromStatus,omitempty"`
	ToStatus   string    `json:"toStatus,omitempty"`
	ActorName  string    `json:"actorName,omitempty"`
	At         time.Time `json:"at"`
}
