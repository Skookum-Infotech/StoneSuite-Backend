// Package requisition: relational store for the Requisition module — an
// internal request-to-buy, raised before a vendor or price is finalized,
// that a controller approves and then converts into a Purchase Order. A
// sibling of estimate/quote/salesorder/invoice/purchaseorder, not the
// generic v1 JSONB workflow engine.
// Spec: docs/superpowers/specs/2026-08-01-requisition-module-design.md
package requisition

import "time"

// LineInput is one requested line on create/update. InventoryItemUUID
// selects a catalog item (the server snapshots its sku/name/description/
// unit/price); omit it for a free-text line, in which case Description is
// required (AD-3).
type LineInput struct {
	LineNumber         int     `json:"lineNumber"`
	InventoryItemUUID  string  `json:"inventoryItemUuid"`
	Description        string  `json:"description"`
	Quantity           float64 `json:"quantity"`
	EstimatedUnitPrice float64 `json:"estimatedUnitPrice"`
}

// requisitionFields is the header payload shared by create and update.
type requisitionFields struct {
	RequestedByEmployeeID int            `json:"requestedByEmployeeId"`
	Department            string         `json:"department"`
	NeededByDate          string         `json:"neededByDate"` // "yyyy-mm-dd"
	Priority              string         `json:"priority"`     // low | normal | high | urgent
	Memo                  string         `json:"memo"`
	VendorUUID            string         `json:"vendorUuid"` // suggestion only (AD-2), nullable
	PaymentTermsID        *int           `json:"paymentTermsId"`
	SalesTaxPercent       float64        `json:"salesTaxPercent"`
	CustomFields          map[string]any `json:"customFields"`
	Items                 []LineInput    `json:"items"`
}

// CreateRequisitionInput is the create-request payload.
type CreateRequisitionInput struct {
	requisitionFields
}

// UpdateRequisitionInput is the update-request payload (a requisition has no
// fixed-at-creation field the way a PO's vendor is fixed — AD-2's suggested
// vendor may be changed freely while still in DRFT).
type UpdateRequisitionInput struct {
	requisitionFields
}

// VendorRef is the light suggested-vendor reference on a Requisition response.
type VendorRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Number string `json:"number,omitempty"`
}

// Line is one requested line in the API response — the frozen snapshot
// values (AD-3), not live inventory_item data.
type Line struct {
	ID                 string  `json:"id"`
	LineNumber         int     `json:"lineNumber"`
	InventoryItemID    *string `json:"inventoryItemId,omitempty"`
	SKU                string  `json:"sku"`
	ItemName           string  `json:"itemName"`
	Description        string  `json:"description"`
	UnitCode           string  `json:"unitCode"`
	Quantity           float64 `json:"quantity"`
	EstimatedUnitPrice float64 `json:"estimatedUnitPrice"`
	EstimatedAmount    float64 `json:"estimatedAmount"`
}

// Requisition is the full API response for a requisition header (+ lines,
// when loaded by Get). OwnerUserID backs the controller's IDOR scope check
// (AD-5: the requester is the scope owner) and is never serialized.
type Requisition struct {
	ID             string `json:"id"`
	Number         string `json:"requisitionNumber"`
	Status         string `json:"status"`         // human label, e.g. "Draft"
	StatusCode     string `json:"statusCode"`     // lkp_record_status code, e.g. "DRFT"
	ApprovalStatus string `json:"approvalStatus"` // none | pending | approved
	OwnerUserID    string `json:"-"`

	RequestedByEmployeeID int    `json:"requestedByEmployeeId"`
	Department            string `json:"department"`
	NeededByDate          string `json:"neededByDate,omitempty"`
	Priority              string `json:"priority"`
	Memo                  string `json:"memo,omitempty"`

	Vendor         *VendorRef `json:"vendor,omitempty"`
	PaymentTermsID *int       `json:"paymentTermsId"`

	CustomFields map[string]any `json:"customFields,omitempty"`

	SalesTaxPercent float64 `json:"salesTaxPercent"`
	Subtotal        float64 `json:"subtotal"`
	TaxTotal        float64 `json:"taxTotal"`
	EstimatedTotal  float64 `json:"estimatedTotal"`

	ConvertedPurchaseOrderID string `json:"convertedPurchaseOrderId,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Items     []Line    `json:"items,omitempty"`
}

// ConvertInput is the convert-to-purchase-order request payload. VendorUUID
// is required (AD-8): a requisition's vendor is only ever a suggestion, so
// the caller/UI must decide the PO's real, mandatory vendor.
type ConvertInput struct {
	VendorUUID string `json:"vendorUuid"`
}

// Page is one page of a keyset-paginated requisition search. List rows omit
// Items (search selects header columns only, to avoid an N+1 line-item
// join) — only Get loads the full requisition with lines.
type Page struct {
	Records    []Requisition
	NextCursor string
	HasMore    bool
}
