// Package purchaseorder: relational store for the Purchase Order module — a
// multi-line, approvable purchasing document sent to a single vendor, tracking
// ordered vs. received quantities. A sibling of estimate/quote/salesorder/
// invoice, not the generic v1 JSONB workflow engine.
// Spec: docs/superpowers/specs/2026-07-22-purchase-order-module-design.md
package purchaseorder

import "time"

// AddressInput is the ship-to (deliver-to) snapshot block on create/update —
// the buyer's receiving address. POs carry a single address block; the
// bill-to is the tenant itself (spec §3.1).
type AddressInput struct {
	Name      string `json:"name"`
	Attention string `json:"attention"`
	AddrLine1 string `json:"addrLine1"`
	AddrLine2 string `json:"addrLine2"`
	SuiteUnit string `json:"suiteUnit"`
	City      string `json:"city"`
	StateID   *int   `json:"stateId"`
	CountryID *int   `json:"countryId"`
	Zip       string `json:"zip"`
	Phone     string `json:"phone"`
	Fax       string `json:"fax"`
	Email     string `json:"email"`
}

// LineInput is one ordered line on create/update. InventoryItemUUID selects a
// catalog item (the server snapshots its sku/name/description/unit/price/tax);
// omit it for a free-text line, in which case Description is required (AD-3).
type LineInput struct {
	LineNumber        int     `json:"lineNumber"`
	InventoryItemUUID string  `json:"inventoryItemUuid"`
	Description       string  `json:"description"`
	Quantity          float64 `json:"quantity"`
	UnitPrice         float64 `json:"unitPrice"`
	DiscountPercent   float64 `json:"discountPercent"`
	TaxRateID         *int    `json:"taxRateId"`
}

// purchaseOrderFields is the header payload shared by create and update
// (everything except the vendor, which is fixed at creation and never changes).
type purchaseOrderFields struct {
	ReferenceNumber string         `json:"referenceNumber"`
	OrderDate       string         `json:"orderDate"`    // "yyyy-mm-dd"
	ExpectedDate    string         `json:"expectedDate"` // "yyyy-mm-dd"
	PaymentTermsID  *int           `json:"paymentTermsId"`
	CurrencyID      *int           `json:"currencyId"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId"`
	SalesTaxPercent float64        `json:"salesTaxPercent"`
	Memo            string         `json:"memo"`
	Notes           string         `json:"notes"`
	InternalNotes   string         `json:"internalNotes"`
	TermsConditions string         `json:"termsConditions"`
	ShipTo          AddressInput   `json:"shipTo"`
	ShippingCharge  float64        `json:"shippingCharge"`
	Adjustment      float64        `json:"adjustment"`
	CustomFields    map[string]any `json:"customFields"`
	Items           []LineInput    `json:"items"`
}

// CreatePurchaseOrderInput is the create-request payload (spec §4).
type CreatePurchaseOrderInput struct {
	VendorUUID string `json:"vendorUuid"`
	purchaseOrderFields
}

// UpdatePurchaseOrderInput mirrors CreatePurchaseOrderInput minus the vendor
// (a purchase order's vendor is fixed after creation — AD-2).
type UpdatePurchaseOrderInput struct {
	purchaseOrderFields
}

// VendorRef is the light vendor reference on a PurchaseOrder response.
type VendorRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Number string `json:"number,omitempty"`
}

// Line is one ordered line in the API response — the frozen snapshot values
// (AD-3), not live inventory_item data. QtyReceived is the receiving hook
// (AD-4), written only by future Item Receipt postings.
type Line struct {
	ID              string  `json:"id"`
	LineNumber      int     `json:"lineNumber"`
	InventoryItemID *string `json:"inventoryItemId,omitempty"`
	SKU             string  `json:"sku"`
	ItemName        string  `json:"itemName"`
	Description     string  `json:"description"`
	UnitCode        string  `json:"unitCode"`
	Quantity        float64 `json:"quantity"`
	QtyReceived     float64 `json:"qtyReceived"`
	UnitPrice       float64 `json:"unitPrice"`
	DiscountPercent float64 `json:"discountPercent"`
	TaxPercent      float64 `json:"taxPercent"`
	LineSubtotal    float64 `json:"lineSubtotal"`
	LineDiscount    float64 `json:"lineDiscount"`
	LineTax         float64 `json:"lineTax"`
	LineTotal       float64 `json:"lineTotal"`
}

// PurchaseOrder is the full API response for a purchase order header (+
// lines, when loaded by Get). OwnerUserID backs the controller's IDOR scope
// check and is never serialized.
type PurchaseOrder struct {
	ID              string    `json:"id"`
	Number          string    `json:"purchaseOrderNumber"`
	Status          string    `json:"status"`         // human label, e.g. "Draft"
	StatusCode      string    `json:"statusCode"`     // lkp_record_status code, e.g. "DRFT"
	ApprovalStatus  string    `json:"approvalStatus"` // none | pending | approved
	Vendor          VendorRef `json:"vendor"`
	OwnerUserID     string    `json:"-"`
	OrderDate       string    `json:"orderDate"`
	ExpectedDate    string    `json:"expectedDate,omitempty"`
	ReferenceNumber string    `json:"referenceNumber,omitempty"`
	Memo            string    `json:"memo,omitempty"`
	Notes           string    `json:"notes,omitempty"`
	InternalNotes   string    `json:"internalNotes,omitempty"`
	TermsConditions string    `json:"termsConditions,omitempty"`

	PaymentTermsID  *int    `json:"paymentTermsId"`
	CurrencyID      *int    `json:"currencyId"`
	OwnerEmployeeID *int    `json:"ownerEmployeeId"`
	SalesTaxPercent float64 `json:"salesTaxPercent"`

	ShipTo AddressInput `json:"shipTo"`

	CustomFields map[string]any `json:"customFields,omitempty"`

	Subtotal       float64   `json:"subtotal"`
	DiscountTotal  float64   `json:"discountTotal"`
	TaxTotal       float64   `json:"taxTotal"`
	ShippingCharge float64   `json:"shippingCharge"`
	Adjustment     float64   `json:"adjustment"`
	GrandTotal     float64   `json:"grandTotal"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Items          []Line    `json:"items,omitempty"`
}

// Page is one page of a keyset-paginated purchase order search. List rows
// omit Items (search selects header columns only, to avoid an N+1 line-item
// join) — only Get loads the full purchase order with lines.
type Page struct {
	Records    []PurchaseOrder
	NextCursor string
	HasMore    bool
}
