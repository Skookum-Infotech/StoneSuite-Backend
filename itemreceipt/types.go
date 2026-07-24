// Package itemreceipt: relational store for the Item Receipt module — the
// document recording goods physically arriving against a finalized purchase
// order. It is the only writer of purchase_order_item.qty_received, and it
// drives the purchase order's SENT → PART → RCVD rollup via the helper the
// Purchase Order module left for it (purchaseorder.RollupReceiptStatus).
// A sibling of purchaseorder/estimate/invoice, not the generic v1 JSONB
// workflow engine.
// Spec: docs/superpowers/specs/2026-07-23-item-receipt-module-design.md
package itemreceipt

import "time"

// LineInput is one arriving line on create/update. PurchaseOrderItemUUID is
// required: a receipt line always traces back to an ordered line (AD-1), which
// is what makes the receiving rollup and the future Vendor Bill 3-way match
// possible. QtyRejected is the damaged/refused portion of QtyReceived — it is
// recorded on the document but never enters stock.
type LineInput struct {
	LineNumber            int     `json:"lineNumber"`
	PurchaseOrderItemUUID string  `json:"purchaseOrderItemUuid"`
	QtyReceived           float64 `json:"qtyReceived"`
	QtyRejected           float64 `json:"qtyRejected"`
	LineNotes             string  `json:"lineNotes"`
}

// itemReceiptFields is the header payload shared by create and update
// (everything except the purchase order, which is fixed at creation).
type itemReceiptFields struct {
	ReceiptDate     string         `json:"receiptDate"` // "yyyy-mm-dd"
	WarehouseID     *int           `json:"warehouseId"` // defaults to the tenant's default warehouse
	PackingSlip     string         `json:"packingSlip"`
	Carrier         string         `json:"carrier"`
	TrackingNumber  string         `json:"trackingNumber"`
	BillOfLading    string         `json:"billOfLading"`
	Notes           string         `json:"notes"`
	InternalNotes   string         `json:"internalNotes"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId"`
	CustomFields    map[string]any `json:"customFields"`
	Items           []LineInput    `json:"items"`
}

// CreateItemReceiptInput is the create-request payload. The vendor is not
// accepted from the caller — it is inherited from the purchase order and
// snapshotted, so a receipt can never name a different counterparty than the
// order it settles.
type CreateItemReceiptInput struct {
	PurchaseOrderUUID string `json:"purchaseOrderUuid"`
	itemReceiptFields
}

// UpdateItemReceiptInput mirrors CreateItemReceiptInput minus the purchase
// order (a receipt's source order is fixed after creation).
type UpdateItemReceiptInput struct {
	itemReceiptFields
}

// PostInput is the /post request payload. OverReceiptReason is required when
// the posting exceeds the tolerance and the caller holds the
// item_receipt:approve override (AD-3).
type PostInput struct {
	OverReceiptReason string `json:"overReceiptReason"`
}

// VoidInput is the /void request payload.
type VoidInput struct {
	VoidReason string `json:"voidReason"`
}

// VendorRef is the light vendor reference on an ItemReceipt response.
type VendorRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Number string `json:"number,omitempty"`
}

// PurchaseOrderRef is the light source-order reference on a response.
type PurchaseOrderRef struct {
	ID         string `json:"id"`
	Number     string `json:"number,omitempty"`
	StatusCode string `json:"statusCode,omitempty"`
}

// Line is one received line in the API response — frozen snapshot values, not
// live purchase_order_item or inventory_item data. QtyOrdered and
// QtyReceivedToDate are the exception: they are read live from the PO line, as
// progress indicators rather than part of this document's own record, so they
// move as later receipts post.
type Line struct {
	ID                  string  `json:"id"`
	LineNumber          int     `json:"lineNumber"`
	PurchaseOrderItemID string  `json:"purchaseOrderItemId"`
	InventoryItemID     *string `json:"inventoryItemId,omitempty"`
	SKU                 string  `json:"sku"`
	ItemName            string  `json:"itemName"`
	Description         string  `json:"description"`
	UnitCode            string  `json:"unitCode"`
	QtyReceived         float64 `json:"qtyReceived"`
	QtyRejected         float64 `json:"qtyRejected"`
	QtyOrdered          float64 `json:"qtyOrdered"`
	QtyReceivedToDate   float64 `json:"qtyReceivedToDate"`
	LineNotes           string  `json:"lineNotes,omitempty"`
}

// ItemReceipt is the full API response for a receipt header (+ lines, when
// loaded by Get). OwnerUserID backs the controller's IDOR scope check and is
// never serialized.
type ItemReceipt struct {
	ID            string           `json:"id"`
	Number        string           `json:"itemReceiptNumber"`
	Status        string           `json:"status"`     // human label, e.g. "Pending"
	StatusCode    string           `json:"statusCode"` // lkp_record_status code, e.g. "PEND"
	PurchaseOrder PurchaseOrderRef `json:"purchaseOrder"`
	Vendor        VendorRef        `json:"vendor"`
	OwnerUserID   string           `json:"-"`

	WarehouseID    int    `json:"warehouseId"`
	WarehouseName  string `json:"warehouseName,omitempty"`
	ReceiptDate    string `json:"receiptDate"`
	PackingSlip    string `json:"packingSlip,omitempty"`
	Carrier        string `json:"carrier,omitempty"`
	TrackingNumber string `json:"trackingNumber,omitempty"`
	BillOfLading   string `json:"billOfLading,omitempty"`
	Notes          string `json:"notes,omitempty"`
	InternalNotes  string `json:"internalNotes,omitempty"`

	OwnerEmployeeID *int `json:"ownerEmployeeId"`

	PostedAt          *time.Time `json:"postedAt,omitempty"`
	VoidedAt          *time.Time `json:"voidedAt,omitempty"`
	VoidReason        string     `json:"voidReason,omitempty"`
	OverReceiptReason string     `json:"overReceiptReason,omitempty"`

	CustomFields map[string]any `json:"customFields,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Items     []Line    `json:"items,omitempty"`
}

// Page is one page of a keyset-paginated item receipt search. List rows omit
// Items (search selects header columns only, to avoid an N+1 line join) —
// only Get loads the full receipt with lines.
type Page struct {
	Records    []ItemReceipt
	NextCursor string
	HasMore    bool
}
