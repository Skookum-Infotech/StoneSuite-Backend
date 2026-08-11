// Package vendorbill implements the minimal Vendor Bill header module — the
// settlement target for the Vendor Payment module built on this branch (spec
// AD-1). It has no line items, no PO linkage, and no bill-owned approval or
// payment ledger; those live entirely in vendorpayment.
package vendorbill

import "time"

// VendorRef is the flattened {id, name} for the bill's fixed vendor (spec
// AD-14: the vendor is fixed at creation, name snapshotted).
type VendorRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// VendorBill is the vendor bill header.
type VendorBill struct {
	ID     string `json:"id"`
	Number string `json:"vendorBillNumber"`

	StatusCode string `json:"statusCode"`
	StatusName string `json:"status"`

	Vendor VendorRef `json:"vendor"`

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`

	ReferenceNumber string     `json:"referenceNumber"`
	BillDate        time.Time  `json:"billDate"`
	DueDate         *time.Time `json:"dueDate,omitempty"`
	Memo            string     `json:"memo"`
	InternalNotes   string     `json:"internalNotes"`

	GrandTotal float64 `json:"grandTotal"`
	AmountPaid float64 `json:"amountPaid"`
	BalanceDue float64 `json:"balanceDue"`

	CustomFields map[string]any `json:"customFields"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// CreateVendorBillInput is the request payload for POST /api/tenant/vendor-bills.
type CreateVendorBillInput struct {
	VendorUUID      string         `json:"vendorUuid"`
	ReferenceNumber string         `json:"referenceNumber"`
	BillDate        *time.Time     `json:"billDate,omitempty"`
	DueDate         *time.Time     `json:"dueDate,omitempty"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	GrandTotal      float64        `json:"grandTotal"`
	Memo            string         `json:"memo"`
	InternalNotes   string         `json:"internalNotes"`
	CustomFields    map[string]any `json:"customFields"`
}

// UpdateVendorBillInput is the request payload for PATCH
// /api/tenant/vendor-bills/{uuid}. Notice it has no GrandTotal field — Update
// only touches non-monetary header fields; the money rollups
// (amount_paid/balance_due) are the sole domain of RecomputeBalance.
type UpdateVendorBillInput struct {
	ReferenceNumber string         `json:"referenceNumber"`
	BillDate        *time.Time     `json:"billDate,omitempty"`
	DueDate         *time.Time     `json:"dueDate,omitempty"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	Memo            string         `json:"memo"`
	InternalNotes   string         `json:"internalNotes"`
	CustomFields    map[string]any `json:"customFields"`
}

// Page is one page of vendor bills plus keyset-pagination state.
type Page struct {
	Records    []VendorBill `json:"records"`
	NextCursor string       `json:"nextCursor"`
	HasMore    bool         `json:"hasMore"`
}
