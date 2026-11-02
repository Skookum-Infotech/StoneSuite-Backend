// Package replenishment is the Reorder page's backend: it finds items whose
// average monthly usage will outrun the stock on hand plus what is already on
// order, and turns the shortfall into draft purchase orders, one per vendor.
//
// Nothing here is stored. The list is worked out from the books every time it
// is asked for, so it can never show a shortage a draft PO has already covered:
// a draft counts as on order the moment it exists.
//
// Usage is what physically left stock over the last full calendar months:
//
//   - A slab-tracked item counts SLABS: the full slabs consumed (cut or used by
//     a fabrication job), read from the slab ledger. A remnant cut from a slab
//     is not counted again when it is used.
//   - Any other stock-tracked item counts its own unit, from the bulk ledger's
//     'consumed' rows (written when a sales order line is filled).
//
// On hand and on order are counted in the same terms, so Needed is a like for
// like figure. The work is item level, not per warehouse: a purchase order has
// no location of its own to attribute "on order at X" to.
package replenishment

// HistoryMonths is how many full calendar months before the current one are
// averaged into the monthly requirement.
const HistoryMonths = 3

// SlabUnit is the unit label shown for a slab-tracked item.
const SlabUnit = "slabs"

// MonthUsage is how much of an item was used in one calendar month.
type MonthUsage struct {
	Month string  `json:"month"` // YYYY-MM
	Used  float64 `json:"used"`
}

// OpenOrder is one purchase order that still has some of an item to arrive.
type OpenOrder struct {
	ID        string  `json:"id"` // external uuid
	Number    string  `json:"number"`
	Status    string  `json:"status"`
	Remaining float64 `json:"remaining"`
}

// Item is one item that stock plus open orders will not carry through next
// month. Needed is always > 0 — a covered item never appears.
type Item struct {
	InventoryItemID string `json:"inventoryItemId"` // external uuid
	SKU             string `json:"sku"`
	Name            string `json:"name"`
	Tracking        string `json:"tracking"`
	Unit            string `json:"unit"` // SlabUnit, or the item's own unit code

	// Usage has one entry per history month, oldest first.
	Usage              []MonthUsage `json:"usage"`
	MonthlyRequirement float64      `json:"monthlyRequirement"`
	OnHand             float64      `json:"onHand"`
	OnOrder            float64      `json:"onOrder"`
	Needed             float64      `json:"needed"`

	// OpenOrders lists the purchase orders behind OnOrder. The controller drops
	// it for a caller who cannot read every purchase order.
	OpenOrders []OpenOrder `json:"openOrders,omitempty"`
}

// Result is the whole Reorder list.
type Result struct {
	// Months are the history months the usage columns cover, oldest first.
	Months []string `json:"months"`
	Items  []Item   `json:"items"`
}

// itemFacts is one item's raw numbers, loaded with plain SQL (see load.go).
// computeItems is a pure function over this shape, so the math worth testing
// exhaustively has no database to fake.
type itemFacts struct {
	id       string
	sku      string
	name     string
	tracking string
	unit     string
	usage    []float64 // one per history month, oldest first
	onHand   float64
	onOrder  float64
	orders   []OpenOrder
}
