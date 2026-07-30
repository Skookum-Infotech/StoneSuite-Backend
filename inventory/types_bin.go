package inventory

import "time"

// Unit kinds, mirroring chk_slab_unit_kind. A bundle is deliberately NOT a unit
// kind — it is its own table, because a bundle has no area of its own and must
// never reach inventory_slab_ledger (spec AD-5).
const (
	UnitKindSlab    = "slab"
	UnitKindRemnant = "remnant"
)

// Bin is a physical location inside a warehouse.
type Bin struct {
	ID            string    `json:"id"`
	WarehouseID   string    `json:"warehouseId"`
	WarehouseName string    `json:"warehouseName"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	ParentID      *string   `json:"parentId,omitempty"`
	Path          string    `json:"path"`
	Depth         int       `json:"depth"`
	CapacityUnits int       `json:"capacityUnits"`
	CapacityArea  float64   `json:"capacityArea"`
	IsActive      bool      `json:"isActive"`
	IsSystem      bool      `json:"isSystem"`
	Notes         string    `json:"notes"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`

	// UnitCount is the number of live units currently in this bin. Populated by
	// the tree and list reads so the UI can show occupancy against capacity.
	UnitCount int `json:"unitCount"`
	// OverCapacity is advisory only. Capacity never blocks a move: a yard crew
	// that has physically placed a slab cannot be blocked by a row count, and a
	// hard block reliably makes them invent a junk bin to work around it —
	// worse data than an accurate flag.
	OverCapacity bool `json:"overCapacity"`

	// Children is populated only by BinTree.
	Children []Bin `json:"children,omitempty"`
}

// BinInput is the write shape. ParentUUID empty means a top-level bin.
type BinInput struct {
	WarehouseUUID string  `json:"warehouseId"`
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	ParentUUID    *string `json:"parentId,omitempty"`
	CapacityUnits int     `json:"capacityUnits"`
	CapacityArea  float64 `json:"capacityArea"`
	IsActive      bool    `json:"isActive"`
	Notes         string  `json:"notes"`
}

// BinPage is one page of a keyset-paginated bin search.
type BinPage struct {
	Records    []Bin  `json:"records"`
	NextCursor string `json:"nextCursor"`
	HasMore    bool   `json:"hasMore"`
}
