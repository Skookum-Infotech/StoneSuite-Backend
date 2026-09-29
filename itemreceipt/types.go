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
//
// Slabs is required for a serialized (slab-tracked) item and forbidden for any
// other: such a line is received slab by slab, its QtyReceived is the computed
// sum of the slabs' area (whatever the caller sent is ignored) and it has no
// QtyRejected -- a refused slab is simply not entered.
type LineInput struct {
	LineNumber            int         `json:"lineNumber"`
	PurchaseOrderItemUUID string      `json:"purchaseOrderItemUuid"`
	QtyReceived           float64     `json:"qtyReceived"`
	QtyRejected           float64     `json:"qtyRejected"`
	LineNotes             string      `json:"lineNotes"`
	Slabs                 []SlabInput `json:"slabs"`
}

// SlabInput is one physical slab arriving on a serialized line. Serial and
// area are deliberately absent: the serial is assigned by the server when the
// receipt posts (PO number + running suffix) and the area is computed from the
// millimetres into the item's own unit -- neither is ever taken from a client.
type SlabInput struct {
	LengthMM     float64 `json:"lengthMm"`
	WidthMM      float64 `json:"widthMm"`
	ThicknessMM  float64 `json:"thicknessMm"`
	BinID        string  `json:"binId"` // bin uuid; blank = not binned yet
	BlockID      string  `json:"blockId"`
	Lot          string  `json:"lot"`
	Grade        string  `json:"grade"`
	SupplierCode string  `json:"supplierCode"`
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
//
// Post asks for the receipt to be posted in the same transaction that creates
// it, so it is never left Pending; OverReceiptReason accompanies it exactly as
// on /post. Neither is read by Create -- the controller checks the caller may
// post and dispatches to CreateAndPost.
type CreateItemReceiptInput struct {
	PurchaseOrderUUID string `json:"purchaseOrderUuid"`
	Post              bool   `json:"post"`
	OverReceiptReason string `json:"overReceiptReason"`
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
	// Slabs is set only on a serialized line.
	Slabs []LineSlab `json:"slabs,omitempty"`
}

// LineSlab is one slab on a received line. Serial, UnitID and UnitStatus stay
// empty until the receipt posts and the slab exists in inventory; UnitStatus is
// then the slab's LIVE status (it may have been reserved or cut since).
type LineSlab struct {
	Serial       string  `json:"serial,omitempty"`
	LengthMM     float64 `json:"lengthMm"`
	WidthMM      float64 `json:"widthMm"`
	ThicknessMM  float64 `json:"thicknessMm"`
	Area         float64 `json:"area"`
	BinID        *string `json:"binId,omitempty"`
	BinPath      string  `json:"binPath,omitempty"`
	BlockID      string  `json:"blockId,omitempty"`
	Lot          string  `json:"lot,omitempty"`
	Grade        string  `json:"grade,omitempty"`
	SupplierCode string  `json:"supplierCode,omitempty"`
	UnitID       *string `json:"unitId,omitempty"`
	UnitStatus   string  `json:"unitStatus,omitempty"`
}

// SlabSequence is the preview of how the next slabs received against a
// purchase order will be numbered: Prefix + a zero-padded running number
// starting at Next. It is advisory -- the real serials are assigned under the
// order's row lock when the receipt posts.
type SlabSequence struct {
	Prefix string `json:"prefix"`
	Next   int    `json:"next"`
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
