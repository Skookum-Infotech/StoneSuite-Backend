// Package inventorycount is the cycle-count / physical stock-take document
// (ICNT).
//
// Statuses: DRFT -> CNTG -> RVW_ -> APPV -> POST, or CANC.
//
// Freezing (DRFT -> CNTG) snapshots what the system believes onto every line
// and blocks movement in the counted scope. The snapshot is the whole point: a
// variance only means something against the number the system held AT THE
// MOMENT COUNTING STARTED. Recomputing it at post time would quietly absorb
// every movement that happened while the crew walked the yard, so a genuine
// shortage would reconcile itself to zero and the write-off would never be
// raised.
package inventorycount

import "time"

// Count is one stock-take document.
type Count struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	StatusID   int    `json:"statusId"`
	StatusCode string `json:"statusCode"`
	StatusName string `json:"statusName"`

	WarehouseID   int    `json:"warehouseId"`
	WarehouseName string `json:"warehouseName,omitempty"`

	// BinID scopes the count to one bin and everything under it. Nil counts the
	// whole warehouse.
	BinID   *string `json:"binId,omitempty"`
	BinPath string  `json:"binPath,omitempty"`

	Date          string  `json:"date"`
	FrozenAt      *string `json:"frozenAt,omitempty"`
	Notes         string  `json:"notes,omitempty"`
	InternalNotes string  `json:"internalNotes,omitempty"`
	OwnerID       *int    `json:"ownerId,omitempty"`

	PostedAt     *string `json:"postedAt,omitempty"`
	CancelledAt  *string `json:"cancelledAt,omitempty"`
	CancelReason string  `json:"cancelReason,omitempty"`

	// Progress figures for the review screen.
	LineCount     int     `json:"lineCount"`
	CountedCount  int     `json:"countedCount"`
	VarianceCount int     `json:"varianceCount"`
	NetVariance   float64 `json:"netVariance"`

	Lines []Line `json:"lines,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Line is one countable thing and what the crew found.
type Line struct {
	ID         string `json:"id"`
	LineNumber int    `json:"lineNumber"`

	InventoryItemID   string `json:"inventoryItemId"`
	InventoryItemName string `json:"inventoryItemName,omitempty"`
	SKU               string `json:"sku,omitempty"`

	InventoryUnitID *string `json:"inventoryUnitId,omitempty"`
	UnitSerial      string  `json:"unitSerial,omitempty"`
	BinPath         string  `json:"binPath,omitempty"`

	UnitCode   string `json:"unitCode,omitempty"`
	ReasonID   *int   `json:"reasonId,omitempty"`
	ReasonName string `json:"reasonName,omitempty"`

	SystemQty float64 `json:"systemQty"`
	// CountedQty is nil until the line is actually counted. That is NOT the same
	// as zero: collapsing the two would write off every shelf the crew simply
	// had not reached yet.
	CountedQty *float64 `json:"countedQty,omitempty"`
	Variance   *float64 `json:"variance,omitempty"`

	IsUnexpected bool    `json:"isUnexpected"`
	CountedAt    *string `json:"countedAt,omitempty"`
	Notes        string  `json:"notes,omitempty"`
}

// Input is the create/update shape. Lines are not supplied — they are built by
// the freeze, from what the system actually holds.
type Input struct {
	WarehouseID   int     `json:"warehouseId"`
	BinID         *string `json:"binId,omitempty"`
	Date          string  `json:"date"`
	Notes         string  `json:"notes"`
	InternalNotes string  `json:"internalNotes"`
	OwnerID       *int    `json:"ownerId,omitempty"`
}

// CountEntry records one line's physical count.
//
// Found is the serialized shorthand: a slab is either on the rack or it is not,
// so a scanner sends found=true and the store fills in the slab's own area.
// CountedQty is the bulk form. Supplying neither is an error rather than a
// silent no-op.
type CountEntry struct {
	LineID     string   `json:"lineId"`
	CountedQty *float64 `json:"countedQty,omitempty"`
	Found      *bool    `json:"found,omitempty"`
	ReasonID   *int     `json:"reasonId,omitempty"`
	Notes      string   `json:"notes"`
}

// UnexpectedEntry records a unit found in the counted scope that the frozen
// snapshot did not contain.
type UnexpectedEntry struct {
	InventoryUnitID string `json:"inventoryUnitId"`
	ReasonID        *int   `json:"reasonId,omitempty"`
	Notes           string `json:"notes"`
}

// Page is one page of a keyset-paginated count search.
type Page struct {
	Records    []Count `json:"records"`
	NextCursor string  `json:"nextCursor"`
	HasMore    bool    `json:"hasMore"`
}

// HistoryEntry is one recorded status event.
type HistoryEntry struct {
	Action     string `json:"action"`
	FromStatus string `json:"fromStatus,omitempty"`
	ToStatus   string `json:"toStatus,omitempty"`
	At         string `json:"at"`
	ByName     string `json:"byName"`
}
