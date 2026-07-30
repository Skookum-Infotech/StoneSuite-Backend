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
}

// Page is one page of a keyset-paginated item search.
type Page struct {
	Records    []Item `json:"records"`
	NextCursor string `json:"nextCursor"`
	HasMore    bool   `json:"hasMore"`
}
