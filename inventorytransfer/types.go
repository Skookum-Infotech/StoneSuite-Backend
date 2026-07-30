// Package inventorytransfer moves stock between warehouses (ITRF).
//
// Genuinely two-legged: stock leaves the source when the transfer ships and
// arrives at the destination when it is received. Between those two moments it
// is in neither warehouse, so inventory_stock UNDERSTATES total on-hand by the
// in-transit quantity — by design. The alternative is a phantom in-transit
// warehouse row that every stock, valuation and reorder query would have to
// learn to exclude, and one that forgets is a silent overstatement.
//
// Statuses: DRFT -> PAPV -> APPV -> TRNS -> RCVD, or CANC before shipping.
package inventorytransfer

import "time"

// Transfer is one warehouse-to-warehouse movement.
type Transfer struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	StatusID   int    `json:"statusId"`
	StatusCode string `json:"statusCode"`
	StatusName string `json:"statusName"`

	FromWarehouseID   int    `json:"fromWarehouseId"`
	FromWarehouseName string `json:"fromWarehouseName,omitempty"`
	ToWarehouseID     int    `json:"toWarehouseId"`
	ToWarehouseName   string `json:"toWarehouseName,omitempty"`

	ToBinID   *string `json:"toBinId,omitempty"`
	ToBinPath string  `json:"toBinPath,omitempty"`

	Date           string  `json:"date"`
	ExpectedDate   *string `json:"expectedDate,omitempty"`
	Carrier        string  `json:"carrier,omitempty"`
	TrackingNumber string  `json:"trackingNumber,omitempty"`
	Notes          string  `json:"notes,omitempty"`
	InternalNotes  string  `json:"internalNotes,omitempty"`
	OwnerID        *int    `json:"ownerId,omitempty"`

	ShippedAt    *string `json:"shippedAt,omitempty"`
	ReceivedAt   *string `json:"receivedAt,omitempty"`
	CancelledAt  *string `json:"cancelledAt,omitempty"`
	CancelReason string  `json:"cancelReason,omitempty"`

	// TotalQty is the sum of the line quantities, in each line's own unit.
	TotalQty float64 `json:"totalQty"`

	Lines []Line `json:"lines,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Line is one item moving. InventoryUnitID set means a specific slab; unset
// means a quantity of a bulk item.
type Line struct {
	ID         string `json:"id"`
	LineNumber int    `json:"lineNumber"`

	InventoryItemID   string `json:"inventoryItemId"`
	InventoryItemName string `json:"inventoryItemName,omitempty"`
	SKU               string `json:"sku,omitempty"`

	InventoryUnitID *string `json:"inventoryUnitId,omitempty"`
	UnitSerial      string  `json:"unitSerial,omitempty"`

	UnitCode string  `json:"unitCode,omitempty"`
	Qty      float64 `json:"qty"`
	Notes    string  `json:"notes,omitempty"`
}

// Input is the create/update shape.
type Input struct {
	FromWarehouseID int         `json:"fromWarehouseId"`
	ToWarehouseID   int         `json:"toWarehouseId"`
	ToBinID         *string     `json:"toBinId,omitempty"`
	Date            string      `json:"date"`
	ExpectedDate    *string     `json:"expectedDate,omitempty"`
	Carrier         string      `json:"carrier"`
	TrackingNumber  string      `json:"trackingNumber"`
	Notes           string      `json:"notes"`
	InternalNotes   string      `json:"internalNotes"`
	OwnerID         *int        `json:"ownerId,omitempty"`
	Lines           []LineInput `json:"lines"`
}

// LineInput is one line on the way in.
//
// Qty is ignored for a serialized line: the quantity is the slab's own area,
// read from inventory_slab. A slab moves whole or not at all, so a partial
// quantity would be meaningless even if it were trustworthy.
type LineInput struct {
	InventoryItemID string  `json:"inventoryItemId"`
	InventoryUnitID *string `json:"inventoryUnitId,omitempty"`
	Qty             float64 `json:"qty"`
	Notes           string  `json:"notes"`
}

// Page is one page of a keyset-paginated transfer search.
type Page struct {
	Records    []Transfer `json:"records"`
	NextCursor string     `json:"nextCursor"`
	HasMore    bool       `json:"hasMore"`
}

// HistoryEntry is one recorded status event.
type HistoryEntry struct {
	Action     string `json:"action"`
	FromStatus string `json:"fromStatus,omitempty"`
	ToStatus   string `json:"toStatus,omitempty"`
	At         string `json:"at"`
	ByName     string `json:"byName"`
}
