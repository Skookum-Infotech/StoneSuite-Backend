package inventory

import "time"

// Tracking modes for an item. 'quantity' items move through inventory_ledger in
// bulk; 'serialized' items are tracked as individual inventory units (slabs and
// remnants) through inventory_slab_ledger.
//
// The distinction matters because BOTH ledgers drive the same inventory_stock
// row: without this discriminator nothing stops an item receiving stock through
// both paths and double-counting.
const (
	TrackingQuantity   = "quantity"
	TrackingSerialized = "serialized"
)

// Item represents an inventory item — the catalogue definition of a product,
// not a physical piece. The physical pieces are Units.
type Item struct {
	ID           string         `json:"id"`
	SKU          string         `json:"sku"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	UnitID       int            `json:"unitId"`
	UnitPrice    float64        `json:"unitPrice"`
	CurrencyID   *int           `json:"currencyId,omitempty"`
	TaxRateID    *int           `json:"taxRateId,omitempty"`
	IsActive     bool           `json:"isActive"`
	CustomFields map[string]any `json:"customFields"`

	// Stone attributes. Typed columns rather than custom_fields JSONB so they
	// are filterable, sortable, joinable and FK-validated — see spec AD-3.
	Tracking           string  `json:"tracking"`
	MaterialID         *int    `json:"materialId,omitempty"`
	ColorID            *int    `json:"colorId,omitempty"`
	FinishID           *int    `json:"finishId,omitempty"`
	ThicknessMM        float64 `json:"thicknessMm"`
	OriginCountryID    *int    `json:"originCountryId,omitempty"`
	Barcode            string  `json:"barcode"`
	DefaultWarehouseID *int    `json:"defaultWarehouseId,omitempty"`

	// TrackStock says whether a sales order line for this item is checked against
	// stock and reserves it. False for an item with no stock to run out of (a
	// service, delivery or labour charge). Always true for a slab-tracked item.
	TrackStock bool `json:"trackStock"`

	// Kits are the kits this item is in. Filled by Get only — list and search
	// rows leave it out, so a catalogue page costs no extra query per row.
	Kits []KitRef `json:"kits,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateItemInput is the write shape for an item.
//
// Update reuses it and overwrites every field: this endpoint has always had
// PUT semantics despite the PATCH verb, and the existing frontend sends the
// whole object. Changing it to true partial-update semantics would silently
// alter the contract for the fields that already behave this way, so it stays
// a whole-object write and is a separate decision.
type CreateItemInput struct {
	SKU          string         `json:"sku"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	UnitID       int            `json:"unitId"`
	UnitPrice    float64        `json:"unitPrice"`
	CurrencyID   *int           `json:"currencyId,omitempty"`
	TaxRateID    *int           `json:"taxRateId,omitempty"`
	CustomFields map[string]any `json:"customFields"`

	Tracking           string  `json:"tracking"`
	MaterialID         *int    `json:"materialId,omitempty"`
	ColorID            *int    `json:"colorId,omitempty"`
	FinishID           *int    `json:"finishId,omitempty"`
	ThicknessMM        float64 `json:"thicknessMm"`
	OriginCountryID    *int    `json:"originCountryId,omitempty"`
	Barcode            string  `json:"barcode"`
	DefaultWarehouseID *int    `json:"defaultWarehouseId,omitempty"`

	// TrackStock is a pointer so an omitted field means "leave as it is" — a
	// plain bool would read a client that predates the field as "turn off".
	// Nil on create means tracked.
	TrackStock *bool `json:"trackStock,omitempty"`

	// KitIDs, when present, makes exactly these kits (external uuids) the item's
	// kits. Absent (nil) leaves its memberships alone — an older client that
	// predates kits must not empty them by saving — while an explicit empty list
	// takes the item out of every kit.
	KitIDs []string `json:"kitIds,omitempty"`
}

// Page is one page of a keyset-paginated item search.
type Page struct {
	Records    []Item `json:"records"`
	NextCursor string `json:"nextCursor"`
	HasMore    bool   `json:"hasMore"`
}
