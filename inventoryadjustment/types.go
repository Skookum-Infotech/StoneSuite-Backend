// Package inventoryadjustment is the manual stock-correction document (IADJ):
// damage, shrinkage, breakage, recounts and found stock.
//
// It is the only path that moves stock without a physical document behind it —
// no vendor shipped it, no customer took it — which is exactly why every line
// carries a mandatory reason code. An adjustment with no reason is a number
// nobody can defend at audit.
//
// Statuses: DRFT -> PAPV -> APPV -> POST, or CANC. Posting is irreversible;
// correcting a posted adjustment means raising the opposite one, so the trail
// keeps both.
package inventoryadjustment

import "time"

// Adjustment is one stock-correction document.
type Adjustment struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	StatusID   int    `json:"statusId"`
	StatusCode string `json:"statusCode"`
	StatusName string `json:"statusName"`

	WarehouseID   int    `json:"warehouseId"`
	WarehouseName string `json:"warehouseName,omitempty"`

	Date          string `json:"date"`
	ReasonID      *int   `json:"reasonId,omitempty"`
	ReasonName    string `json:"reasonName,omitempty"`
	Notes         string `json:"notes,omitempty"`
	InternalNotes string `json:"internalNotes,omitempty"`
	OwnerID       *int   `json:"ownerId,omitempty"`
	OwnerName     string `json:"ownerName,omitempty"`

	PostedAt     *string `json:"postedAt,omitempty"`
	CancelledAt  *string `json:"cancelledAt,omitempty"`
	CancelReason string  `json:"cancelReason,omitempty"`

	// NetDelta is the sum of the lines' signed deltas. Reported so a reviewer
	// sees the document's total stock effect without adding it up by eye.
	NetDelta float64 `json:"netDelta"`

	Lines []Line `json:"lines,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Line is one item's correction. A line is either bulk or serialized:
// InventoryUnitID set means "this specific slab", unset means "this many of a
// quantity-tracked item".
type Line struct {
	ID         string `json:"id"`
	LineNumber int    `json:"lineNumber"`

	InventoryItemID   string `json:"inventoryItemId"`
	InventoryItemName string `json:"inventoryItemName,omitempty"`
	SKU               string `json:"sku,omitempty"`

	InventoryUnitID *string `json:"inventoryUnitId,omitempty"`
	UnitSerial      string  `json:"unitSerial,omitempty"`

	ReasonID   int    `json:"reasonId"`
	ReasonName string `json:"reasonName,omitempty"`

	UnitCode string  `json:"unitCode,omitempty"`
	QtyDelta float64 `json:"qtyDelta"`
	Notes    string  `json:"notes,omitempty"`
}

// Input is the create/update shape.
type Input struct {
	WarehouseID   int         `json:"warehouseId"`
	Date          string      `json:"date"`
	ReasonID      *int        `json:"reasonId,omitempty"`
	Notes         string      `json:"notes"`
	InternalNotes string      `json:"internalNotes"`
	OwnerID       *int        `json:"ownerId,omitempty"`
	Lines         []LineInput `json:"lines"`
}

// LineInput is one line on the way in.
//
// QtyDelta is ignored for a serialized line: the delta is the slab's own area,
// read at post time from inventory_slab and never taken from the caller. Nothing
// forces slab_area_unit_id to equal the item's unit, so a client-supplied area
// is how a SQM measurement lands against a SQFT item — wrong by 10.76x with no
// constraint that would catch it.
type LineInput struct {
	InventoryItemID string  `json:"inventoryItemId"`
	InventoryUnitID *string `json:"inventoryUnitId,omitempty"`
	ReasonID        int     `json:"reasonId"`
	QtyDelta        float64 `json:"qtyDelta"`
	Notes           string  `json:"notes"`
}

// Page is one page of a keyset-paginated adjustment search.
type Page struct {
	Records    []Adjustment `json:"records"`
	NextCursor string       `json:"nextCursor"`
	HasMore    bool         `json:"hasMore"`
}
