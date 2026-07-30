package inventory

import "time"

// Bundle statuses, mirroring chk_bundle_status.
//
// The machine is open -> sealed -> broken, and 'broken' is terminal. Re-banding
// a split bundle creates a NEW bundle with a new code, because the physical band
// tag is what a yard crew reads off the pallet: reviving an old code would point
// at a stack that no longer matches the paperwork.
const (
	BundleOpen   = "open"   // members may be added and removed
	BundleSealed = "sealed" // banded: membership frozen, members move together
	BundleBroken = "broken" // band cut: members detached and independent again
)

// Bundle is a set of slabs banded to one pallet, sawn from the same block and
// handled as a unit until the band is cut.
//
// It has no area of its own and never reaches inventory_slab_ledger — only its
// members do. That is why it is its own table rather than an inventory_slab row
// with a 'bundle' kind: every stock and valuation query would otherwise have to
// remember "AND slab_unit_kind <> 'bundle'", and one forgotten predicate would
// silently double the on-hand area of the whole yard (spec AD-5).
type Bundle struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Status       string `json:"status"`
	VendorID     *int   `json:"vendorId,omitempty"`
	SupplierCode string `json:"supplierCode,omitempty"`
	BlockID      string `json:"blockId,omitempty"`
	Lot          string `json:"lot,omitempty"`

	InventoryItemID   *string `json:"inventoryItemId,omitempty"`
	InventoryItemName string  `json:"inventoryItemName,omitempty"`

	WarehouseID   int     `json:"warehouseId"`
	WarehouseName string  `json:"warehouseName,omitempty"`
	BinID         *string `json:"binId,omitempty"`
	BinPath       string  `json:"binPath,omitempty"`

	ReceivedAt *string `json:"receivedAt,omitempty"`
	Notes      string  `json:"notes,omitempty"`

	// MemberCount and TotalArea cover live members only. TotalArea is safe to
	// sum because every member shares the bundle's item — see AddBundleMembers,
	// which adopts the first member's item and then holds the rest to it.
	MemberCount int     `json:"memberCount"`
	TotalArea   float64 `json:"totalArea"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// BundleInput is the write shape for create and update.
//
// Status is deliberately absent: it moves only through Seal and Break, so that
// a careless PATCH of the whole object cannot silently unband a pallet.
//
// On update, an omitted InventoryItemUUID or BinUUID (JSON null or absent)
// leaves that field untouched; send "" to clear it. Those two alone get this
// treatment because the server can set them behind the caller's back — a bundle
// adopts its first member's item, and MoveBundle rewrites the bin — so a form
// that round-trips a stale copy would otherwise undo it. Every other field is
// owned end to end by the caller and is written as sent.
type BundleInput struct {
	Code              string  `json:"code"`
	VendorID          *int    `json:"vendorId,omitempty"`
	SupplierCode      string  `json:"supplierCode"`
	BlockID           string  `json:"blockId"`
	Lot               string  `json:"lot"`
	InventoryItemUUID *string `json:"inventoryItemId,omitempty"`
	WarehouseID       int     `json:"warehouseId"`
	BinUUID           *string `json:"binId,omitempty"`
	ReceivedAt        *string `json:"receivedAt,omitempty"`
	Notes             string  `json:"notes"`
	// MemberIDs optionally seeds membership at creation, so receiving a banded
	// pallet of seven slabs is one call rather than eight.
	MemberIDs []string `json:"memberIds,omitempty"`
}

// BundleMemberInput carries the unit uuids to attach or detach.
type BundleMemberInput struct {
	MemberIDs []string `json:"memberIds"`
	Note      string   `json:"note"`
}

// MoveBundleInput relocates a whole bundle within its warehouse.
type MoveBundleInput struct {
	BinUUID *string `json:"binId"`
	Note    string  `json:"note"`
}
