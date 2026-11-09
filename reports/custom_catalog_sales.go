package reports

import "stonesuite-backend/authz"

const (
	moduleSales     = "sales"
	moduleCRM       = "crm"
	modulePurchases = "purchases"
	moduleInventory = "inventory"
)

// docHeader describes one sales document table that every sales source hangs
// off (the header is always aliased "h").
type docHeader struct {
	table, pfx, recordType string
}

var (
	hdrQuote    = docHeader{"quote", "quote", "QUOT"}
	hdrEstimate = docHeader{"estimate", "estimate", "ESTM"}
	hdrOrder    = docHeader{"sales_order", "sales_order", "SORD"}
	hdrInvoice  = docHeader{"invoice", "invoice", "INVC"}
)

// col is "h.<pfx>_<name>".
func (d docHeader) col(name string) string { return "h." + d.pfx + "_" + name }

// docJoins are the joins every sales document (or its lines) may need.
func (d docHeader) docJoins(withItem bool) (map[string]joinSpec, []string) {
	defs := []joinDef{
		{name: "status", sql: "LEFT JOIN lkp_record_status rs ON rs.record_status_id = " + d.col("status")},
		{name: "owner", sql: "LEFT JOIN employee ow ON ow.employee_id = " + d.col("owner_id")},
		{name: "rep", sql: "LEFT JOIN employee sr ON sr.employee_id = " + d.col("sales_rep_id")},
		{name: "customer", sql: "LEFT JOIN customer c ON c.customer_id = " + d.col("customer_id")},
		{name: "ctype", sql: "LEFT JOIN lkp_customer_type ct ON ct.customer_type_id = c.customer_type", needs: []string{"customer"}},
		{name: "cstate", sql: "LEFT JOIN lkp_state cst ON cst.state_id = c.customer_addr_state", needs: []string{"customer"}},
		{name: "csource", sql: "LEFT JOIN lkp_crm_lead_source cls ON cls.lead_source_id = c.customer_lead_source", needs: []string{"customer"}},
	}
	if withItem {
		defs = append(defs, itemJoins("l.inventory_item_id")...)
	}
	return buildJoins(defs...)
}

// itemJoins joins the inventory item a line (or stock/slab row) points at.
func itemJoins(itemIDExpr string) []joinDef {
	return []joinDef{
		{name: "item", sql: "LEFT JOIN inventory_item ii ON ii.inventory_item_id = " + itemIDExpr},
		{name: "imat", sql: "LEFT JOIN lkp_material im ON im.material_id = ii.inventory_item_material_id", needs: []string{"item"}},
		{name: "iunit", sql: "LEFT JOIN lkp_unit iu ON iu.unit_id = ii.inventory_item_unit_id", needs: []string{"item"}},
	}
}

func customerLinked() []Field {
	return linked("Customer",
		text("customer.name", "Customer name", "c.customer_name", "customer"),
		text("customer.number", "Customer number", "c.customer_doc_num", "customer"),
		text("customer.type", "Customer type", "ct.customer_type_name", "ctype"),
		text("customer.city", "Customer city", "c.customer_addr_city", "customer"),
		text("customer.state", "Customer state", "cst.state_name", "cstate"),
		text("customer.lead_source", "Customer lead source", "cls.lead_source_name", "csource"),
	)
}

func itemLinked() []Field {
	return linked("Item",
		text("item.name", "Item name", "ii.inventory_item_name", "item"),
		text("item.sku", "Item SKU", "ii.inventory_item_sku", "item"),
		text("item.material", "Item material", "im.material_name", "imat"),
		text("item.unit", "Item unit", "iu.unit_code", "iunit"),
		text("item.tracking", "Item tracking", "ii.inventory_item_tracking", "item"),
	)
}

// docOwn are the header's own fields; extra lists the ones only some
// documents have.
func (d docHeader) docOwn(extra ...Field) []Field {
	base := []Field{
		text("number", "Number", d.col("number"), ""),
		date("date", "Date", d.col("date"), ""),
		status("status", "Status", "rs.record_status_name", "status", d.recordType),
		text("po_number", "PO number", d.col("po_number"), ""),
		text("reference", "Reference number", d.col("reference_number"), ""),
		money("subtotal", "Subtotal", d.col("subtotal"), ""),
		money("discount_total", "Discount", d.col("discount_total"), ""),
		money("tax_total", "Tax", d.col("tax_total"), ""),
		money("shipping", "Shipping", d.col("shipping_charge"), ""),
		money("grand_total", "Grand total", d.col("grand_total"), ""),
		text("owner", "Owner", personName("ow"), "owner"),
		text("sales_rep", "Sales rep", personName("sr"), "rep"),
		date("created", "Created", d.col("created_at")+"::date", ""),
	}
	return concat(base, extra)
}

func (d docHeader) docSource(key, label string, extra ...Field) Source {
	joins, order := d.docJoins(false)
	return Source{
		Key: key, Label: label, Module: moduleSales, Resource: authz.ResourceReportSales,
		from:      d.table + " h",
		where:     []string{d.col("deleted_at") + " IS NULL"},
		ownerExpr: d.col("owner_id"),
		joins:     joins, joinOrder: order,
		Fields: concat(d.docOwn(extra...), customerLinked()),
	}
}

// lineLinked are the header fields a line source exposes.
func (d docHeader) lineLinked(group, prefix string, extra ...Field) []Field {
	base := []Field{
		text(prefix+".number", group+" number", d.col("number"), ""),
		date(prefix+".date", group+" date", d.col("date"), ""),
		status(prefix+".status", group+" status", "rs.record_status_name", "status", d.recordType),
		text(prefix+".owner", group+" owner", personName("ow"), "owner"),
	}
	return linked(group, concat(base, extra)...)
}

func (d docHeader) lineSource(key, label, lineTable, fk, group, prefix string, lineOwn []Field, extra ...Field) Source {
	joins, order := d.docJoins(true)
	own := []Field{
		text("item_name", "Line item name", "l.item_name", ""),
		text("sku", "Line SKU", "l.sku", ""),
		text("unit_code", "Line unit", "l.unit_code", ""),
		number("quantity", "Quantity", "l.quantity", ""),
		money("unit_price", "Unit price", "l.unit_price", ""),
		number("discount_percent", "Discount %", "l.discount_percent", ""),
		money("line_subtotal", "Line subtotal", "l.line_subtotal", ""),
		money("line_discount", "Line discount", "l.line_discount", ""),
		money("net_amount", "Net amount", "(l.line_subtotal - l.line_discount)", ""),
		money("line_tax", "Line tax", "l.line_tax", ""),
		money("line_total", "Line total", "l.line_total", ""),
		text("kit_name", "Kit", "l.kit_name", ""),
	}
	return Source{
		Key: key, Label: label, Module: moduleSales, Resource: authz.ResourceReportSales,
		from:      lineTable + " l JOIN " + d.table + " h ON h." + d.pfx + "_id = l." + fk,
		where:     []string{d.col("deleted_at") + " IS NULL", "l.item_deleted_at IS NULL"},
		ownerExpr: d.col("owner_id"),
		joins:     joins, joinOrder: order,
		Fields: concat(own, lineOwn, d.lineLinked(group, prefix, extra...), customerLinked(), itemLinked()),
	}
}

func salesSources() []Source {
	quotes := hdrQuote.docSource("quotes", "Quotes", date("valid_until", "Valid until", "h.quote_valid_until", ""))
	estimates := hdrEstimate.docSource("estimates", "Estimates", date("valid_until", "Valid until", "h.estimate_valid_until", ""))
	orders := hdrOrder.docSource("sales_orders", "Sales orders",
		date("expected_delivery", "Expected delivery", "h.sales_order_expected_delivery", ""),
		date("install_date", "Scheduled install date", "h.sales_order_scheduled_install_date", ""))
	orderLines := hdrOrder.lineSource("sales_order_lines", "Sales order lines", "sales_order_item", "sales_order_id",
		"Sales order", "order",
		[]Field{number("fulfilled_quantity", "Fulfilled quantity", "l.line_fulfilled_quantity", "")},
		date("order.expected_delivery", "Sales order expected delivery", "h.sales_order_expected_delivery", ""))
	invoices := hdrInvoice.docSource("invoices", "Invoices",
		date("due_date", "Due date", "h.invoice_due_date", ""),
		money("amount_paid", "Amount paid", "h.invoice_amount_paid", ""),
		money("balance_due", "Balance due", "h.invoice_balance_due", ""),
		money("credit_total", "Credits applied", "h.invoice_credit_total", ""))
	invoiceLines := hdrInvoice.lineSource("invoice_lines", "Invoice lines", "invoice_item", "invoice_id",
		"Invoice", "invoice", nil,
		date("invoice.due_date", "Invoice due date", "h.invoice_due_date", ""))
	return []Source{quotes, estimates, orders, orderLines, invoices, invoiceLines, paymentsSource()}
}

func paymentsSource() Source {
	joins, order := buildJoins(
		joinDef{name: "status", sql: "LEFT JOIN lkp_record_status rs ON rs.record_status_id = h.payment_status"},
		joinDef{name: "method", sql: "LEFT JOIN lkp_payment_method pm ON pm.payment_method_id = h.payment_method"},
		joinDef{name: "owner", sql: "LEFT JOIN employee ow ON ow.employee_id = h.payment_owner_id"},
		joinDef{name: "customer", sql: "LEFT JOIN customer c ON c.customer_id = h.payment_customer_id"},
		joinDef{name: "ctype", sql: "LEFT JOIN lkp_customer_type ct ON ct.customer_type_id = c.customer_type", needs: []string{"customer"}},
		joinDef{name: "cstate", sql: "LEFT JOIN lkp_state cst ON cst.state_id = c.customer_addr_state", needs: []string{"customer"}},
		joinDef{name: "csource", sql: "LEFT JOIN lkp_crm_lead_source cls ON cls.lead_source_id = c.customer_lead_source", needs: []string{"customer"}},
	)
	own := []Field{
		text("number", "Number", "h.payment_number", ""),
		date("date", "Date", "h.payment_date", ""),
		status("status", "Status", "rs.record_status_name", "status", "PYMT"),
		text("method", "Payment method", "pm.payment_method_name", "method"),
		text("reference", "Reference number", "h.payment_reference_number", ""),
		money("amount", "Amount", "h.payment_amount", ""),
		money("applied_total", "Applied to invoices", "h.payment_applied_total", ""),
		money("unapplied_amount", "Unapplied", "h.payment_unapplied_amount", ""),
		money("refunded_total", "Refunded", "h.payment_refunded_total", ""),
		text("owner", "Owner", personName("ow"), "owner"),
		date("created", "Created", "h.payment_created_at::date", ""),
	}
	return Source{
		Key: "payments", Label: "Payments", Module: moduleSales, Resource: authz.ResourceReportSales,
		from:      "payment h",
		where:     []string{"h.payment_deleted_at IS NULL"},
		ownerExpr: "h.payment_owner_id",
		joins:     joins, joinOrder: order,
		Fields: concat(own, customerLinked()),
	}
}
