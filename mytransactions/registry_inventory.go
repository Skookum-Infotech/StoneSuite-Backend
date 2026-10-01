package mytransactions

import "stonesuite-backend/authz"

// inventorySources lists the stock modules. None of them narrows a read grant
// to "own" (their own searches ignore scope — see globalsearch/providers_inventory.go),
// so Owner is empty and a read grant sees every row.
func inventorySources() []Source {
	item := Source{
		Key: "inventory_item", Label: "Inventory Item", Resource: authz.ResourceInventoryItem,
		Domain: "inventory", Module: "item",
		Table:     "inventory_item",
		ID:        "t.inventory_item_uuid",
		Number:    "t.inventory_item_sku",
		Name:      "t.inventory_item_name",
		CreatedBy: "inventory_item_created_by", UpdatedBy: "inventory_item_updated_by",
		CreatedAt: "inventory_item_created_at", UpdatedAt: "inventory_item_updated_at",
		DeletedAt: "inventory_item_deleted_at",
	}

	// A unit (slab/remnant) keeps its status as a plain text enum, not a lookup row.
	unit := Source{
		Key: "inventory_unit", Label: "Inventory Unit", Resource: authz.ResourceInventoryUnit,
		Domain: "inventory", Module: "unit",
		Table:      "inventory_slab",
		ID:         "t.inventory_slab_uuid",
		Number:     "t.slab_serial",
		StatusName: "INITCAP(t.slab_status)",
		StatusCode: "t.slab_status",
		CreatedBy:  "slab_created_by", UpdatedBy: "slab_updated_by",
		CreatedAt: "slab_created_at", UpdatedAt: "slab_updated_at",
		DeletedAt: "slab_deleted_at",
	}

	adjustment := stockDoc("inventory_adjustment", "Inventory Adjustment", authz.ResourceInventoryAdjustment, "adjustment", "adjustment")
	transfer := stockDoc("inventory_transfer", "Inventory Transfer", authz.ResourceInventoryTransfer, "transfer", "transfer")
	count := stockDoc("inventory_count", "Inventory Count", authz.ResourceInventoryCount, "count", "count")

	return []Source{item, unit, adjustment, transfer, count}
}

// stockDoc is doc for an inventory document whose columns use a short prefix
// (<p>_number, <p>_status) while its uuid column keeps the table's full name
// (inventory_<p>_uuid); the inventory modules apply no own-scope narrowing.
func stockDoc(key, label string, res authz.Resource, module, p string) Source {
	s := doc(key, label, res, "inventory", module, key, p)
	s.ID = "t." + key + "_uuid"
	s.Owner = ""
	return s
}
