// Package vendorcredit implements the relational Vendor Credit module: the
// accounts-payable mirror of creditmemo/ -- a header-only instrument (no
// lines, no tax, no discount, AD-1) that reduces a vendor's outstanding
// vendor_bill balance via a live application ledger. A sibling of
// vendorbill/vendorpayment, not the generic v1 JSONB workflow engine.
// Spec: docs/superpowers/specs/2026-08-13-vendor-credit-module-design.md
package vendorcredit

import "time"

// VendorRef is the flattened {id, name} for "who owes us this credit" navigation.
type VendorRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Application is one live vendor_credit_application row, joined with its
// vendor bill's display fields.
type Application struct {
	ID               string    `json:"id"`
	VendorBillID     string    `json:"vendorBillId"`
	VendorBillNumber string    `json:"vendorBillNumber"`
	Amount           float64   `json:"amount"`
	CreatedAt        time.Time `json:"createdAt"`
}

// VendorCredit is the vendor credit header + its live applications.
type VendorCredit struct {
	ID     string `json:"id"`
	Number string `json:"vendorCreditNumber"`

	StatusCode string `json:"statusCode"`
	StatusName string `json:"status"`

	Vendor VendorRef `json:"vendor"`

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`

	ReferenceNumber string    `json:"referenceNumber"`
	Reason          string    `json:"reason"`
	Memo            string    `json:"memo"`
	InternalNotes   string    `json:"internalNotes"`
	CreditDate      time.Time `json:"creditDate"`

	GrandTotal      float64 `json:"grandTotal"`
	AppliedTotal    float64 `json:"appliedTotal"`
	UnappliedAmount float64 `json:"unappliedAmount"`

	CustomFields map[string]any `json:"customFields"`
	Applications []Application  `json:"applications"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// CreateVendorCreditInput is the request payload for POST
// /api/tenant/vendor-credits.
type CreateVendorCreditInput struct {
	VendorUUID      string         `json:"vendorUuid"`
	ReferenceNumber string         `json:"referenceNumber"`
	CreditDate      string         `json:"creditDate,omitempty"` // "yyyy-mm-dd"; blank => CURRENT_DATE
	Reason          string         `json:"reason"`
	Memo            string         `json:"memo"`
	InternalNotes   string         `json:"internalNotes"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	Amount          float64        `json:"amount"`
	CustomFields    map[string]any `json:"customFields"`
}

// UpdateVendorCreditInput is the request payload for PATCH
// /api/tenant/vendor-credits/{uuid}. The vendor is fixed at creation (AD-12)
// and is not part of this input. Amount is a pointer so a PATCH that omits it
// leaves the stored amount unchanged (mirrors creditmemo.UpdateCreditMemoInput's
// Adjustment field).
type UpdateVendorCreditInput struct {
	ReferenceNumber string         `json:"referenceNumber"`
	CreditDate      string         `json:"creditDate,omitempty"` // "yyyy-mm-dd"; blank leaves the stored date unchanged
	Reason          string         `json:"reason"`
	Memo            string         `json:"memo"`
	InternalNotes   string         `json:"internalNotes"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	Amount          *float64       `json:"amount,omitempty"`
	CustomFields    map[string]any `json:"customFields"`
}

// Page is one page of vendor credits plus keyset-pagination state.
type Page struct {
	Records    []VendorCredit `json:"records"`
	NextCursor string         `json:"nextCursor"`
	HasMore    bool           `json:"hasMore"`
}
