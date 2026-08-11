// Package vendorpayment implements the Vendor Payment module: header +
// application/refund ledger against vendor_bill, approval sign-off, and a
// scheduler for date-triggered dispatch (spec
// docs/superpowers/specs/2026-08-11-vendor-payments-module-design.md). This
// file defines the request/response shapes; Apply/Unapply/RecordRefund/
// Approve/Transition live in sibling files added alongside this core.
package vendorpayment

import "time"

// VendorRef is the flattened {id, name} for the payment's fixed vendor (spec
// AD-14: the vendor is fixed at creation, name snapshotted).
type VendorRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Application is one live vendor_payment_application row, joined with its
// vendor bill's display fields.
type Application struct {
	ID               string    `json:"id"`
	VendorBillID     string    `json:"vendorBillId"`
	VendorBillNumber string    `json:"vendorBillNumber"`
	Amount           float64   `json:"amount"`
	CreatedAt        time.Time `json:"createdAt"`
}

// Refund is one live vendor_payment_refund row, joined with its vendor
// bill's display fields (spec AD-5).
type Refund struct {
	ID               string    `json:"id"`
	VendorBillID     string    `json:"vendorBillId"`
	VendorBillNumber string    `json:"vendorBillNumber"`
	Amount           float64   `json:"amount"`
	Reason           string    `json:"reason"`
	ReferenceNumber  string    `json:"referenceNumber"`
	Memo             string    `json:"memo"`
	RefundedAt       time.Time `json:"refundedAt"`
	CreatedAt        time.Time `json:"createdAt"`
}

// VendorPayment is the vendor payment header + its live applications and
// refunds.
type VendorPayment struct {
	ID     string `json:"id"`
	Number string `json:"vendorPaymentNumber"`

	StatusCode string `json:"statusCode"`
	StatusName string `json:"status"`

	Vendor VendorRef `json:"vendor"`

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`

	MethodID   int    `json:"methodId"`
	MethodName string `json:"method"`

	ReferenceNumber string     `json:"referenceNumber"`
	PaymentDate     time.Time  `json:"paymentDate"`
	ScheduledDate   *time.Time `json:"scheduledDate,omitempty"`
	CurrencyID      *int       `json:"currencyId,omitempty"`
	Memo            string     `json:"memo"`
	InternalNotes   string     `json:"internalNotes"`

	Amount          float64 `json:"amount"`
	AppliedTotal    float64 `json:"appliedTotal"`
	UnappliedAmount float64 `json:"unappliedAmount"`

	ApprovalStatus       string `json:"approvalStatus"`
	ApprovedByEmployeeID *int   `json:"approvedByEmployeeId,omitempty"`

	CustomFields map[string]any `json:"customFields"`
	Applications []Application  `json:"applications"`
	Refunds      []Refund       `json:"refunds"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// ApplicationInput is one entry of a create/apply request.
type ApplicationInput struct {
	VendorBillUUID string  `json:"vendorBillUuid"`
	Amount         float64 `json:"amount"`
}

// CreateVendorPaymentInput is the request payload for POST
// /api/tenant/vendor-payments.
type CreateVendorPaymentInput struct {
	VendorUUID      string             `json:"vendorUuid"`
	MethodID        int                `json:"methodId"`
	ReferenceNumber string             `json:"referenceNumber"`
	PaymentDate     *time.Time         `json:"paymentDate,omitempty"`
	ScheduledDate   *time.Time         `json:"scheduledDate,omitempty"`
	CurrencyID      *int               `json:"currencyId,omitempty"`
	OwnerEmployeeID *int               `json:"ownerEmployeeId,omitempty"`
	Amount          float64            `json:"amount"`
	Memo            string             `json:"memo"`
	InternalNotes   string             `json:"internalNotes"`
	CustomFields    map[string]any     `json:"customFields"`
	Applications    []ApplicationInput `json:"applications"`
}

// UpdateVendorPaymentInput is the request payload for PATCH
// /api/tenant/vendor-payments/{uuid}. Notice it has no VendorUUID, Amount, or
// Applications field — amount is immutable post-creation (spec AD-12) and the
// vendor is fixed at creation (spec AD-14).
type UpdateVendorPaymentInput struct {
	MethodID        int            `json:"methodId"`
	ReferenceNumber string         `json:"referenceNumber"`
	PaymentDate     *time.Time     `json:"paymentDate,omitempty"`
	ScheduledDate   *time.Time     `json:"scheduledDate,omitempty"`
	CurrencyID      *int           `json:"currencyId,omitempty"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	Memo            string         `json:"memo"`
	InternalNotes   string         `json:"internalNotes"`
	CustomFields    map[string]any `json:"customFields"`
}

// Page is one page of vendor payments plus keyset-pagination state.
type Page struct {
	Records    []VendorPayment `json:"records"`
	NextCursor string          `json:"nextCursor"`
	HasMore    bool            `json:"hasMore"`
}
