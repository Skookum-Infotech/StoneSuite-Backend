package reports

import "stonesuite-backend/authz"

// slabStatuses are the values chk_slab_status allows, in lifecycle order.
var slabStatuses = []string{"on_order", "in_transit", "received", "available", "reserved", "consumed", "scrapped"}

// itemOwn are an inventory item's own fields (alias "ii"); material/unit joins
// are the same ones itemJoins builds.
func itemOwn(group string) []Field {
	fields := []Field{
		text("name", "Name", "ii.inventory_item_name", ""),
		text("sku", "SKU", "ii.inventory_item_sku", ""),
		text("material", "Material", "im.material_name", "imat"),
		text("color", "Color", "ic.color_name", "icolor"),
		text("finish", "Finish", "ifn.finish_name", "ifinish"),
		text("unit", "Unit", "iu.unit_code", "iunit"),
		text("tracking", "Tracking", "ii.inventory_item_tracking", ""),
		money("purchase_price", "Purchasing price", "ii.inventory_item_purchase_unit_price", ""),
		money("selling_price", "Selling price", "ii.inventory_item_unit_price", ""),
		date("created", "Created", "ii.inventory_item_created_at::date", ""),
	}
	if group == "" {
		return fields
	}
	for i := range fields {
		fields[i].Key = "item." + fields[i].Key
		fields[i].Label = "Item " + lowerFirst(fields[i].Label)
	}
	return linked(group, fields...)
}

func lowerFirst(s string) string {
	if s == "" || (len(s) > 1 && s[1] >= 'A' && s[1] <= 'Z') {
		return s // keep acronyms such as SKU
	}
	return string(s[0]+('a'-'A')) + s[1:]
}

// itemLookupJoins are the item's lookup joins for a source whose FROM already
// includes the item as "ii" (items, stock, slabs), so there is no "item" join.
func itemLookupJoins() []joinDef {
	return []joinDef{
		{name: "imat", sql: "LEFT JOIN lkp_material im ON im.material_id = ii.inventory_item_material_id"},
		{name: "iunit", sql: "LEFT JOIN lkp_unit iu ON iu.unit_id = ii.inventory_item_unit_id"},
		{name: "icolor", sql: "LEFT JOIN lkp_color ic ON ic.color_id = ii.inventory_item_color_id"},
		{name: "ifinish", sql: "LEFT JOIN lkp_finish ifn ON ifn.finish_id = ii.inventory_item_finish_id"},
	}
}

func itemsSource() Source {
	joins, order := buildJoins(itemLookupJoins()...)
	return Source{
		Key: "items", Label: "Items", Module: moduleInventory, Resource: authz.ResourceReportInventory,
		from:  "inventory_item ii",
		where: []string{"ii.inventory_item_deleted_at IS NULL"},
		joins: joins, joinOrder: order,
		Fields: itemOwn(""),
	}
}

func stockSource() Source {
	joins, order := buildJoins(append(itemLookupJoins(),
		joinDef{name: "location", sql: "LEFT JOIN company_location w ON w.company_location_id = s.warehouse_id"})...)
	own := []Field{
		text("location", "Location", "w.name", "location"),
		number("on_hand", "On hand", "s.quantity_on_hand", ""),
		number("reorder_point", "Reorder point", "s.reorder_point", ""),
		money("value", "Stock value", "(s.quantity_on_hand * ii.inventory_item_purchase_unit_price)", ""),
		date("created", "Created", "s.stock_created_at::date", ""),
	}
	return Source{
		Key: "stock", Label: "Stock by location", Module: moduleInventory, Resource: authz.ResourceReportInventory,
		from:  "inventory_stock s JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id",
		where: []string{"ii.inventory_item_deleted_at IS NULL"},
		joins: joins, joinOrder: order,
		Fields: concat(own, itemOwn("Item")),
	}
}

func slabsSource() Source {
	joins, order := buildJoins(append(itemLookupJoins(),
		joinDef{name: "location", sql: "LEFT JOIN company_location w ON w.company_location_id = sl.warehouse_id"},
		joinDef{name: "bin", sql: "LEFT JOIN inventory_bin ib ON ib.inventory_bin_id = sl.inventory_bin_id"})...)
	slabStatus := Field{Key: "status", Label: "Status", Type: FieldStatus, Options: slabStatuses, expr: "sl.slab_status"}
	own := []Field{
		text("serial", "Serial", "sl.slab_serial", ""),
		slabStatus,
		text("grade", "Grade", "sl.slab_grade", ""),
		text("finish", "Slab finish", "sl.slab_finish", ""),
		text("location", "Location", "w.name", "location"),
		text("bin", "Bin", "ib.bin_code", "bin"),
		number("length_mm", "Length (mm)", "sl.slab_length_mm", ""),
		number("width_mm", "Width (mm)", "sl.slab_width_mm", ""),
		number("thickness_mm", "Thickness (mm)", "sl.slab_thickness_mm", ""),
		number("area", "Area", "sl.slab_area", ""),
		date("received", "Received", "sl.slab_received_at", ""),
		date("created", "Created", "sl.slab_created_at::date", ""),
	}
	return Source{
		Key: "slabs", Label: "Slabs", Module: moduleInventory, Resource: authz.ResourceReportInventory,
		from:  "inventory_slab sl JOIN inventory_item ii ON ii.inventory_item_id = sl.inventory_item_id",
		where: []string{"sl.slab_deleted_at IS NULL", "ii.inventory_item_deleted_at IS NULL"},
		joins: joins, joinOrder: order,
		Fields: concat(own, itemOwn("Item")),
	}
}

func inventorySources() []Source {
	return []Source{itemsSource(), stockSource(), slabsSource()}
}
