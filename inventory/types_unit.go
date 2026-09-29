package inventory

import "time"

// Unit is one physical, individually-tracked piece of stock — a slab or a
// remnant cut from one.
//
// This is schema.org/IndividualProduct to Item's schema.org/Product: Item is
// the catalogue definition ("3cm Absolute Black, polished"), Unit is the actual
// stone in the yard with its own serial, measured dimensions and location.
//
// Backed by inventory_slab. The table keeps its original name because renaming
// it would break inventory_slab_ledger, fabrication_job_slab and every existing
// foreign key, for no gain.
type Unit struct {
	ID           string `json:"id"`
	Serial       string `json:"serial"`
	Kind         string `json:"kind"` // slab | remnant
	VendorID     *int   `json:"vendorId,omitempty"`
	SupplierCode string `json:"supplierCode,omitempty"`
	Barcode      string `json:"barcode,omitempty"`

	InventoryItemID   string `json:"inventoryItemId"`
	InventoryItemName string `json:"inventoryItemName,omitempty"`
	InventoryItemSKU  string `json:"inventoryItemSku,omitempty"`

	WarehouseID   int     `json:"warehouseId"`
	WarehouseName string  `json:"warehouseName,omitempty"`
	BinID         *string `json:"binId,omitempty"`
	BinPath       string  `json:"binPath,omitempty"`

	BundleID   string  `json:"bundleId,omitempty"` // legacy free-text bundle label
	BundleUUID *string `json:"bundleUuid,omitempty"`
	BlockID    string  `json:"blockId,omitempty"`
	Lot        string  `json:"lot,omitempty"`

	LengthMM    float64 `json:"lengthMm"`
	WidthMM     float64 `json:"widthMm"`
	ThicknessMM float64 `json:"thicknessMm"`
	Area        float64 `json:"area"`
	AreaUnitID  int     `json:"areaUnitId"`
	// AreaUnitCode is the code of AreaUnitID (SQFT, SQM...), so a screen can say
	// what Area is measured in without a second lookup.
	AreaUnitCode string `json:"areaUnitCode,omitempty"`

	Form   string `json:"form"`   // full | cut
	Status string `json:"status"` // available|reserved|consumed|scrapped
	// ParentUnitID/RootUnitID are the uuids; the serials sit beside them so the
	// lineage can be shown by name, never as a raw id.
	ParentUnitID *string `json:"parentUnitId,omitempty"`
	ParentSerial string  `json:"parentSerial,omitempty"`
	RootUnitID   *string `json:"rootUnitId,omitempty"`
	RootSerial   string  `json:"rootSerial,omitempty"`

	// Usage is what has become of this unit's stone. It is always present; every
	// figure is zero and every timestamp nil for a unit nobody has touched.
	Usage UnitUsage `json:"usage"`

	IsUsableRemnant bool   `json:"isUsableRemnant"`
	Grade           string `json:"grade,omitempty"`
	Finish          string `json:"finish,omitempty"`
	FinishID        *int   `json:"finishId,omitempty"`
	PhotoKey        string `json:"photoKey,omitempty"`

	// The purchase-order receipt that brought this unit into stock. Nil for a
	// unit that did not arrive through one (a remnant cut in the yard, or a
	// legacy unit received before slabs were tied to receipts).
	ReceiptID     *string `json:"receiptId,omitempty"`
	ReceiptNumber string  `json:"receiptNumber,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// UnitUsage is the consumption picture for one unit: the job it is (or was)
// held for, when it left stock, and how its area split when it was cut.
//
// A unit is never partly used. It is cut whole, so Area leaves stock in full and
// RecoveredArea comes back as new offcut units; what is left, UsedArea, is the
// stone that went into finished product and saw kerf. RecoveredArea and
// UsedArea are therefore only ever non-zero on a consumed unit.
type UnitUsage struct {
	// JobID/JobNumber name the fabrication job holding this unit (reserved) or
	// that cut it (consumed). Empty for a unit no job has claimed — including one
	// cut by hand from the Inventory screen.
	JobID      string     `json:"jobId,omitempty"`
	JobNumber  string     `json:"jobNumber,omitempty"`
	ReservedAt *time.Time `json:"reservedAt,omitempty"`
	// ConsumedAt/ScrappedAt come from the slab ledger, the record of when the
	// stone actually left stock.
	ConsumedAt *time.Time `json:"consumedAt,omitempty"`
	ScrappedAt *time.Time `json:"scrappedAt,omitempty"`

	// OffcutCount and RecoveredArea count the offcuts that came back into stock
	// from this unit when it was cut, whatever became of them afterwards.
	OffcutCount   int     `json:"offcutCount"`
	RecoveredArea float64 `json:"recoveredArea"`
	// UsedArea is Area - RecoveredArea for a consumed unit, 0 otherwise.
	UsedArea float64 `json:"usedArea"`
}

// CreateUnitInput receives a full physical piece. Offcuts are never created
// through this path — they are minted by a cut, which derives their lineage and
// area from the parent.
type CreateUnitInput struct {
	Serial            string  `json:"serial"`
	VendorUUID        *string `json:"vendorId"`
	SupplierCode      string  `json:"supplierCode"`
	Barcode           string  `json:"barcode"`
	InventoryItemUUID string  `json:"inventoryItemId"`
	WarehouseID       int     `json:"warehouseId"`
	BinUUID           *string `json:"binId,omitempty"`
	BundleUUID        *string `json:"bundleUuid,omitempty"`
	BundleID          string  `json:"bundleId"`
	BlockID           string  `json:"blockId"`
	Lot               string  `json:"lot"`
	LengthMM          float64 `json:"lengthMm"`
	WidthMM           float64 `json:"widthMm"`
	ThicknessMM       float64 `json:"thicknessMm"`
	// Area is optional and, when supplied, is IGNORED in favour of the area
	// computed from the millimetre dimensions into the item's own unit. Nothing
	// in the schema forces slab_area_unit_id to equal inventory_item_unit_id, so
	// trusting a client-supplied area is how a SQM measurement ends up ledgered
	// against a SQFT item, wrong by 10.76x with no constraint to catch it.
	Area     float64 `json:"area"`
	Grade    string  `json:"grade"`
	Finish   string  `json:"finish"`
	FinishID *int    `json:"finishId,omitempty"`
}

// UnitPage is one page of a keyset-paginated unit search.
type UnitPage struct {
	Records    []Unit `json:"records"`
	NextCursor string `json:"nextCursor"`
	HasMore    bool   `json:"hasMore"`
}

// MoveUnitInput moves a unit to a different bin within the same warehouse.
type MoveUnitInput struct {
	BinUUID *string `json:"binId"`
	Note    string  `json:"note"`
}
