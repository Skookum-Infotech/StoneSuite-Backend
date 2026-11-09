package reports

import "stonesuite-backend/authz"

// vendorNameSQL is a vendor's display name: legal name for an organization,
// given + family name for a person (same rule as the Purchases reports).
func vendorNameSQL(alias string) string {
	return "CASE WHEN " + alias + ".vendor_type = 'Organization' THEN " + alias + ".vendor_legal_name " +
		"ELSE NULLIF(TRIM(" + alias + ".vendor_given_name || ' ' || " + alias + ".vendor_family_name), '') END"
}

func vendorLinked() []Field {
	return linked("Vendor",
		text("vendor.name", "Vendor name", vendorNameSQL("v"), "vendor"),
		text("vendor.number", "Vendor number", "v.vendor_number", "vendor"),
		text("vendor.type", "Vendor type", "v.vendor_type", "vendor"),
	)
}

// purchaseDoc names one purchasing header table (always aliased "h").
type purchaseDoc struct{ table, pfx, recordType string }

func (d purchaseDoc) col(name string) string { return "h." + d.pfx + "_" + name }

func (d purchaseDoc) joins(withItem bool) (map[string]joinSpec, []string) {
	defs := []joinDef{
		{name: "status", sql: "LEFT JOIN lkp_record_status rs ON rs.record_status_id = " + d.col("status")},
		{name: "owner", sql: "LEFT JOIN employee ow ON ow.employee_id = " + d.col("owner_id")},
		{name: "vendor", sql: "LEFT JOIN vendor v ON v.vendor_id = " + d.col("vendor_id")},
	}
	if withItem {
		defs = append(defs, itemJoins("l.inventory_item_id")...)
	}
	return buildJoins(defs...)
}

func vendorsSource() Source {
	joins, order := buildJoins(
		joinDef{name: "status", sql: "LEFT JOIN lkp_record_status rs ON rs.record_status_id = h.vendor_status"},
		joinDef{name: "owner", sql: "LEFT JOIN employee ow ON ow.employee_id = h.vendor_owner_id"},
	)
	return Source{
		Key: "vendors", Label: "Vendors", Module: modulePurchases, Resource: authz.ResourceReportPurchases,
		from:      "vendor h",
		where:     []string{"h.vendor_deleted_at IS NULL"},
		ownerExpr: "h.vendor_owner_id",
		joins:     joins, joinOrder: order,
		Fields: []Field{
			text("name", "Name", vendorNameSQL("h"), ""),
			text("number", "Number", "h.vendor_number", ""),
			text("type", "Type", "h.vendor_type", ""),
			status("status", "Status", "rs.record_status_name", "status", "VNDR"),
			text("email", "Email", "h.vendor_email", ""),
			text("owner", "Owner", personName("ow"), "owner"),
			date("created", "Created", "h.vendor_created_at::date", ""),
		},
	}
}

func purchaseOrdersSource() Source {
	d := purchaseDoc{"purchase_order", "purchase_order", "PORD"}
	joins, order := d.joins(false)
	own := []Field{
		text("number", "Number", d.col("number"), ""),
		date("date", "Date", d.col("date"), ""),
		date("expected_date", "Expected date", d.col("expected_date"), ""),
		status("status", "Status", "rs.record_status_name", "status", d.recordType),
		text("reference", "Reference number", d.col("reference_number"), ""),
		money("subtotal", "Subtotal", d.col("subtotal"), ""),
		money("discount_total", "Discount", d.col("discount_total"), ""),
		money("tax_total", "Tax", d.col("tax_total"), ""),
		money("shipping", "Shipping", d.col("shipping_charge"), ""),
		money("grand_total", "Grand total", d.col("grand_total"), ""),
		text("owner", "Owner", personName("ow"), "owner"),
		date("created", "Created", d.col("created_at")+"::date", ""),
	}
	return Source{
		Key: "purchase_orders", Label: "Purchase orders", Module: modulePurchases, Resource: authz.ResourceReportPurchases,
		from:      d.table + " h",
		where:     []string{d.col("deleted_at") + " IS NULL"},
		ownerExpr: d.col("owner_id"),
		joins:     joins, joinOrder: order,
		Fields: concat(own, vendorLinked()),
	}
}

func purchaseOrderLinesSource() Source {
	d := purchaseDoc{"purchase_order", "purchase_order", "PORD"}
	joins, order := d.joins(true)
	own := []Field{
		text("item_name", "Line item name", "l.item_name", ""),
		text("sku", "Line SKU", "l.sku", ""),
		text("unit_code", "Line unit", "l.unit_code", ""),
		number("quantity", "Quantity", "l.quantity", ""),
		number("qty_received", "Quantity received", "l.qty_received", ""),
		number("expected_slabs", "Expected slabs", "l.expected_slabs", ""),
		money("unit_price", "Unit cost", "l.unit_price", ""),
		money("line_subtotal", "Line subtotal", "l.line_subtotal", ""),
		money("line_discount", "Line discount", "l.line_discount", ""),
		money("net_amount", "Net amount", "(l.line_subtotal - l.line_discount)", ""),
		money("line_total", "Line total", "l.line_total", ""),
	}
	header := linked("Purchase order",
		text("po.number", "PO number", d.col("number"), ""),
		date("po.date", "PO date", d.col("date"), ""),
		date("po.expected_date", "PO expected date", d.col("expected_date"), ""),
		status("po.status", "PO status", "rs.record_status_name", "status", d.recordType),
		text("po.owner", "PO owner", personName("ow"), "owner"),
	)
	return Source{
		Key: "purchase_order_lines", Label: "Purchase order lines", Module: modulePurchases, Resource: authz.ResourceReportPurchases,
		from:      "purchase_order_item l JOIN purchase_order h ON h.purchase_order_id = l.purchase_order_id",
		where:     []string{d.col("deleted_at") + " IS NULL", "l.item_deleted_at IS NULL"},
		ownerExpr: d.col("owner_id"),
		joins:     joins, joinOrder: order,
		Fields: concat(own, header, vendorLinked(), itemLinked()),
	}
}

func vendorBillsSource() Source {
	d := purchaseDoc{"vendor_bill", "vendor_bill", "VBIL"}
	joins, order := d.joins(false)
	own := []Field{
		text("number", "Number", d.col("number"), ""),
		text("vendor_invoice_number", "Vendor invoice number", d.col("vendor_invoice_number"), ""),
		date("date", "Bill date", d.col("date"), ""),
		date("due_date", "Due date", d.col("due_date"), ""),
		status("status", "Status", "rs.record_status_name", "status", d.recordType),
		money("subtotal", "Subtotal", d.col("subtotal"), ""),
		money("discount_total", "Discount", d.col("discount_total"), ""),
		money("tax_total", "Tax", d.col("tax_total"), ""),
		money("grand_total", "Grand total", d.col("grand_total"), ""),
		money("amount_paid", "Amount paid", d.col("amount_paid"), ""),
		money("balance_due", "Balance due", d.col("balance_due"), ""),
		text("owner", "Owner", personName("ow"), "owner"),
		date("created", "Created", d.col("created_at")+"::date", ""),
	}
	return Source{
		Key: "vendor_bills", Label: "Vendor bills", Module: modulePurchases, Resource: authz.ResourceReportPurchases,
		from:      d.table + " h",
		where:     []string{d.col("deleted_at") + " IS NULL"},
		ownerExpr: d.col("owner_id"),
		joins:     joins, joinOrder: order,
		Fields: concat(own, vendorLinked()),
	}
}

func purchaseSources() []Source {
	return []Source{vendorsSource(), purchaseOrdersSource(), purchaseOrderLinesSource(), vendorBillsSource()}
}
