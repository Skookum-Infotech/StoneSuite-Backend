// Package bintransfer moves slabs between bins inside one location (BTRF).
//
// A bin move is stock-neutral by construction: bins locate serialized units and
// inventory_stock is keyed (item, warehouse), so nothing here touches a ledger.
// What a bin transfer adds over the old instant "Move Bin" is a document: it is
// requested, optionally approved by the people configured under Configuration >
// Workflows > Bin Transfer, and only then applied, so every move has an owner, a
// sign-off trail and a number to look up.
//
// Statuses: DRFT -> PAPV -> APPV -> COMP, or CANC before the stone is moved.
package bintransfer

import "time"

// BinTransfer is one request to move slabs between bins.
type BinTransfer struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	StatusCode string `json:"statusCode"`
	StatusName string `json:"statusName"`
	// ApprovalStatus is none | pending | approved | rejected
	// (approvalchain.Status*), exposed for the status control and the list pill.
	ApprovalStatus string `json:"approvalStatus"`
	// NextStatusCodes are the legal next moves right now, with an approval
	// checkpoint nobody is configured on collapsed out (approvalchain.NextStatusCodes).
	NextStatusCodes []string `json:"nextStatusCodes"`

	WarehouseID   int    `json:"warehouseId"`
	WarehouseName string `json:"warehouseName,omitempty"`

	Date  string `json:"date"`
	Notes string `json:"notes,omitempty"`

	// OwnerUserID is users.id, the IDOR scope owner; OwnerEmployeeID is the
	// employee the approval notifications address.
	OwnerUserID     string `json:"ownerUserId,omitempty"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`
	OwnerName       string `json:"ownerName,omitempty"`

	CompletedAt     *string `json:"completedAt,omitempty"`
	CompletedByName string  `json:"completedByName,omitempty"`
	CancelledAt     *string `json:"cancelledAt,omitempty"`
	CancelReason    string  `json:"cancelReason,omitempty"`

	// SlabCount, TotalArea and ToBinSummary summarise the live lines so a list row
	// needs no second read: the destination is the bin path when every line goes
	// to one bin, otherwise "N bins".
	SlabCount    int     `json:"slabCount"`
	TotalArea    float64 `json:"totalArea"`
	ToBinSummary string  `json:"toBinSummary,omitempty"`

	CustomFields map[string]any `json:"customFields"`
	// Lines is present on a single read and empty on a list row.
	Lines []Line `json:"lines"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// Line is one slab moving to one destination bin.
type Line struct {
	ID         string `json:"id"`
	LineNumber int    `json:"lineNumber"`

	UnitID     string `json:"unitId"`
	UnitSerial string `json:"unitSerial"`
	// UnitStatus is the slab's live status (available, reserved, in_transit...), so
	// a page can warn about a slab that has since been cut or scrapped.
	UnitStatus string `json:"unitStatus"`

	InventoryItemName string  `json:"inventoryItemName,omitempty"`
	Area              float64 `json:"area"`
	AreaUnitCode      string  `json:"areaUnitCode,omitempty"`

	// From* is the bin the slab was in when the line was written. CurrentBinPath is
	// where it is right now, which differs when it was moved some other way while
	// the transfer waited for approval.
	FromBinID      *string `json:"fromBinId,omitempty"`
	FromBinPath    string  `json:"fromBinPath,omitempty"`
	FromBinName    string  `json:"fromBinName,omitempty"`
	CurrentBinPath string  `json:"currentBinPath,omitempty"`

	ToBinID   string `json:"toBinId"`
	ToBinPath string `json:"toBinPath"`
	ToBinName string `json:"toBinName,omitempty"`

	Notes string `json:"notes,omitempty"`
	// MovedAt is set when completing the transfer applied this line.
	MovedAt *string `json:"movedAt,omitempty"`
}

// Input is the create/update shape.
type Input struct {
	WarehouseID     int            `json:"warehouseId"`
	Date            string         `json:"date"`
	Notes           string         `json:"notes"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	CustomFields    map[string]any `json:"customFields"`
	Lines           []LineInput    `json:"lines"`

	// IdempotencyKey (from the Idempotency-Key header, never the body) makes
	// a retried create return the bin transfer the first attempt made.
	IdempotencyKey string `json:"-"`
	// RequestHash fingerprints the body the key arrived with.
	RequestHash string `json:"-"`
	// ExpectedVersion is the recordVersion the client loaded. When sent, an
	// update is refused (ErrVersionConflict) if someone saved in between.
	ExpectedVersion *int `json:"expectedVersion,omitempty"`
}

// LineInput is one line on the way in. UnitID and ToBinID are uuids; the
// from-bin is never accepted from the client, it is read off the slab.
type LineInput struct {
	UnitID  string `json:"unitId"`
	ToBinID string `json:"toBinId"`
	Notes   string `json:"notes"`
}

// Page is one page of a keyset-paginated search.
type Page struct {
	Records    []BinTransfer `json:"records"`
	NextCursor string        `json:"nextCursor"`
	HasMore    bool          `json:"hasMore"`
}

// HistoryEntry is one recorded status event on the document.
type HistoryEntry struct {
	Action     string `json:"action"`
	FromStatus string `json:"fromStatus,omitempty"`
	ToStatus   string `json:"toStatus,omitempty"`
	At         string `json:"at"`
	ByName     string `json:"byName"`
}
