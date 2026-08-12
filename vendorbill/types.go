// Package vendorbill implements the relational Vendor Bill module: the
// accounts-payable mirror of invoice/ -- header + lines + AD-6 approval gate
// + a bill-owned settlement ledger + optional Purchase Order lineage. A
// sibling of estimate/quote/salesorder/invoice/purchaseorder, not the
// generic v1 JSONB workflow engine.
// Spec: docs/superpowers/specs/2026-08-10-vendor-bill-module-design.md
package vendorbill

import "time"

// VendorRef is the flattened {id, name, number} for "who billed us" navigation.
type VendorRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Number string `json:"number,omitempty"`
}

// PurchaseOrderRef is the flattened {id, number} for lineage navigation (AD-8).
type PurchaseOrderRef struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

// Line is one vendor_bill_item row: catalog/free-text snapshot + stored money.
type Line struct {
	ID                  string  `json:"id"`
	LineNumber          int     `json:"lineNumber"`
	InventoryItemID     *string `json:"inventoryItemId,omitempty"`
	PurchaseOrderItemID *string `json:"purchaseOrderItemId,omitempty"`
	SKU                 string  `json:"sku"`
	ItemName            string  `json:"itemName"`
	Description         string  `json:"description"`
	UnitID              *int    `json:"unitId,omitempty"`
	UnitCode            string  `json:"unitCode"`
	Quantity            float64 `json:"quantity"`
	UnitPrice           float64 `json:"unitPrice"`
	DiscountPercent     float64 `json:"discountPercent"`
	TaxRateID           *int    `json:"taxRateId,omitempty"`
	TaxPercent          float64 `json:"taxPercent"`
	LineSubtotal        float64 `json:"lineSubtotal"`
	LineDiscount        float64 `json:"lineDiscount"`
	LineTax             float64 `json:"lineTax"`
	LineTotal           float64 `json:"lineTotal"`
}

// LineInput is one line of a create/update request. There is no
// purchaseOrderItemUuid field here on purpose (AD-8): that lineage FK is set
// exclusively by the convert path, never by manual Create/Update input.
type LineInput struct {
	LineNumber        int     `json:"lineNumber"`
	InventoryItemUUID string  `json:"inventoryItemUuid,omitempty"`
	Description       string  `json:"description,omitempty"`
	Quantity          float64 `json:"quantity"`
	UnitPrice         float64 `json:"unitPrice"`
	DiscountPercent   float64 `json:"discountPercent"`
	TaxRateID         *int    `json:"taxRateId,omitempty"`
}

// BillPayment is one live vendor_bill_payment row (AD-7).
type BillPayment struct {
	ID              string    `json:"id"`
	Amount          float64   `json:"amount"`
	MethodID        *int      `json:"methodId,omitempty"`
	MethodName      string    `json:"method,omitempty"`
	ReferenceNumber string    `json:"referenceNumber"`
	Memo            string    `json:"memo"`
	PaidAt          string    `json:"paidAt"`
	CreatedAt       time.Time `json:"createdAt"`
}

// vendorBillFields is the header payload shared by create and update --
// everything except the vendor, which is fixed at creation and never
// changes (AD-2).
type vendorBillFields struct {
	VendorInvoiceNumber string         `json:"vendorInvoiceNumber"`
	ReferenceNumber     string         `json:"referenceNumber"`
	BillDate            string         `json:"billDate"`          // "yyyy-mm-dd"; blank => CURRENT_DATE
	DueDate             string         `json:"dueDate,omitempty"` // "yyyy-mm-dd"
	PaymentTermsID      *int           `json:"paymentTermsId,omitempty"`
	CurrencyID          *int           `json:"currencyId,omitempty"`
	OwnerEmployeeID     *int           `json:"ownerEmployeeId,omitempty"`
	SalesTaxPercent     float64        `json:"salesTaxPercent"`
	Memo                string         `json:"memo"`
	Notes               string         `json:"notes"`
	InternalNotes       string         `json:"internalNotes"`
	TermsConditions     string         `json:"termsConditions"`
	Adjustment          float64        `json:"adjustment"`
	CustomFields        map[string]any `json:"customFields"`
	Items               []LineInput    `json:"items"`
}

// CreateVendorBillInput is the request payload for POST /api/tenant/vendor-bills.
// Notice it doesn't take purchaseOrderUuid (that is only set via the convert endpoint).
type CreateVendorBillInput struct {
	VendorUUID string `json:"vendorUuid"`
	vendorBillFields
}

// UpdateVendorBillInput mirrors CreateVendorBillInput minus the vendor
// (a vendor bill's vendor is fixed after creation -- AD-2).
type UpdateVendorBillInput struct {
	vendorBillFields
}

// VendorBill is the full API response for a vendor bill header (+ lines,
// + payments, when loaded by Get). OwnerUserID backs the controller's IDOR
// scope check and is never serialized.
type VendorBill struct {
	ID     string `json:"id"`
	Number string `json:"vendorBillNumber"`

	StatusCode     string `json:"statusCode"`
	StatusName     string `json:"status"`
	ApprovalStatus string `json:"approvalStatus"` // none | pending | approved

	Vendor        VendorRef         `json:"vendor"`
	PurchaseOrder *PurchaseOrderRef `json:"purchaseOrder,omitempty"` // nullable lineage (AD-8)

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`

	VendorInvoiceNumber string `json:"vendorInvoiceNumber"`
	ReferenceNumber     string `json:"referenceNumber"`
	BillDate            string `json:"billDate"`
	DueDate             string `json:"dueDate,omitempty"`

	PaymentTermsID  *int    `json:"paymentTermsId,omitempty"`
	CurrencyID      *int    `json:"currencyId,omitempty"`
	ExchangeRate    float64 `json:"exchangeRate"`
	SalesTaxPercent float64 `json:"salesTaxPercent"`

	Memo            string `json:"memo"`
	Notes           string `json:"notes"`
	InternalNotes   string `json:"internalNotes"`
	TermsConditions string `json:"termsConditions"`

	Subtotal      float64 `json:"subtotal"`
	DiscountTotal float64 `json:"discountTotal"`
	TaxTotal      float64 `json:"taxTotal"`
	Adjustment    float64 `json:"adjustment"`
	GrandTotal    float64 `json:"grandTotal"`

	AmountPaid float64 `json:"amountPaid"`
	BalanceDue float64 `json:"balanceDue"`

	CustomFields map[string]any `json:"customFields"`
	Items        []Line         `json:"items"`
	Payments     []BillPayment  `json:"payments,omitempty"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// Page is one page of vendor bills plus keyset-pagination state.
type Page struct {
	Records    []VendorBill `json:"records"`
	NextCursor string       `json:"nextCursor"`
	HasMore    bool         `json:"hasMore"`
}
