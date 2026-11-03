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

	// Length / Width / Slabs give the size of one slab and how many slabs the
	// line covers. With length and width both set the line's quantity is derived
	// from them (see package linedim), so the quantity sent is ignored.
	Length *float64 `json:"length"`
	Width  *float64 `json:"width"`
	Slabs  *int     `json:"slabs"`
	// DimensionUnit is the one unit length and width are measured in (mm, cm, in,
	// ft, m). Blank takes the item's unit, then inches.
	DimensionUnit string `json:"dimensionUnit"`
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

	// IdempotencyKey (from the Idempotency-Key header, never the body) makes
	// a retried create return the requisition the first attempt made.
	IdempotencyKey string `json:"-"`
	// RequestHash fingerprints the body the key arrived with.
	RequestHash string `json:"-"`
}

// UpdateRequisitionInput is the update-request payload (a requisition has no
// fixed-at-creation field the way a PO's vendor is fixed — AD-2's suggested
// vendor may be changed freely while still in DRFT).
type UpdateRequisitionInput struct {
	requisitionFields
	// ExpectedVersion is the recordVersion the client loaded. When sent, the
	// update is refused (ErrVersionConflict) if someone saved in between.
	ExpectedVersion *int `json:"expectedVersion,omitempty"`
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

	// Length / Width / Slabs are the slab size and count the line was priced
	// from, when it was entered that way; the quantity above is their area.
	Length        *float64 `json:"length,omitempty"`
	Width         *float64 `json:"width,omitempty"`
	Slabs         *int     `json:"slabs,omitempty"`
	DimensionUnit string   `json:"dimensionUnit,omitempty"`
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
	// NextStatusCodes are the statuses a manual transition may move the
	// requisition to right now, with an approval checkpoint nobody is
	// configured to approve collapsed out of the path
	// (approvalchain.NextStatusCodes).
	NextStatusCodes []string `json:"nextStatusCodes"`
	OwnerUserID     string   `json:"-"`

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
	// RecordVersion increments on every write; clients echo it back as
	// expectedVersion on update for optimistic locking.
	RecordVersion int    `json:"recordVersion"`
	Items         []Line `json:"items,omitempty"`
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
