package anomaly

// Rule keys for the sales and money-in rules, stored in anomaly_rule_key.
const (
	RuleHighDiscount          = "high_discount"
	RuleBelowCostSale         = "below_cost_sale"
	RuleUnusualOrderTotal     = "unusual_order_total"
	RuleDuplicateInvoice      = "duplicate_invoice"
	RuleOverCreditLimit       = "over_credit_limit"
	RuleOverdueInvoice        = "overdue_invoice"
	RuleStaleUnappliedPayment = "stale_unapplied_payment"
	RuleRefundExceedsPayment  = "refund_exceeds_payment"
	RuleQuickRefund           = "quick_refund"
	RuleCreditMemoBurst       = "credit_memo_burst"
	RuleVoidedAfterPosting    = "voided_after_posting"
	RuleBackdatedDocument     = "backdated_document"
	RulePriceDriftOnConvert   = "price_drift_on_conversion"
	RuleZeroTaxUnusual        = "zero_tax_unusual"
	RuleFilledNotInvoiced     = "filled_not_invoiced"
)

// salesDoc names the columns of one customer-facing document module. The
// four modules are structural twins, so one template covers all of them.
type salesDoc struct {
	display, resource, table, id, uuid, number string
	status, owner, date, created, deleted      string
	customer, grand, itemTable, itemFK         string
}

var salesDocs = []salesDoc{
	{"Quote", "quote", "quote", "quote_id", "quote_uuid", "quote_number",
		"quote_status", "quote_owner_id", "quote_date", "quote_created_at", "quote_deleted_at",
		"quote_customer_id", "quote_grand_total", "quote_item", "quote_id"},
	{"Estimate", "estimate", "estimate", "estimate_id", "estimate_uuid", "estimate_number",
		"estimate_status", "estimate_owner_id", "estimate_date", "estimate_created_at", "estimate_deleted_at",
		"estimate_customer_id", "estimate_grand_total", "estimate_item", "estimate_id"},
	{"Sales Order", "sales_order", "sales_order", "sales_order_id", "sales_order_uuid", "sales_order_number",
		"sales_order_status", "sales_order_owner_id", "sales_order_date", "sales_order_created_at", "sales_order_deleted_at",
		"sales_order_customer_id", "sales_order_grand_total", "sales_order_item", "sales_order_id"},
	{"Invoice", "invoice", "invoice", "invoice_id", "invoice_uuid", "invoice_number",
		"invoice_status", "invoice_owner_id", "invoice_date", "invoice_created_at", "invoice_deleted_at",
		"invoice_customer_id", "invoice_grand_total", "invoice_item", "invoice_id"},
}

// perDoc builds one query per module from a template and unions them.
func perDoc(docs []salesDoc, template string) string {
	parts := make([]string, 0, len(docs))
	for _, d := range docs {
		parts = append(parts, tpl(template,
			"{display}", d.display, "{resource}", d.resource, "{table}", d.table, "{id}", d.id,
			"{uuid}", d.uuid, "{number}", d.number, "{status}", d.status, "{owner}", d.owner,
			"{date}", d.date, "{created}", d.created, "{deleted}", d.deleted, "{customer}", d.customer,
			"{grand}", d.grand, "{item}", d.itemTable, "{fk}", d.itemFK))
	}
	return unionAll(parts)
}

func salesRules() []Rule {
	salesOrder := []salesDoc{salesDocs[2]}
	invoice := []salesDoc{salesDocs[3]}
	soAndInvoice := []salesDoc{salesDocs[2], salesDocs[3]}
	return []Rule{
		// A line discount far above this workspace's norm.
		sqlRule{
			key: RuleHighDiscount, severity: SeverityWarning, title: "%s has an unusually high discount",
			audience: audience{owner: true, superAdmins: true},
			args:     []any{salesInactiveStatuses, discountFloorPercent, discountBaselineMultiple, lookbackDays, discountBaselineMinLines, discountAlwaysFlagPercent},
			query: perDoc(salesDocs, `
				SELECT d.{uuid}::text, d.{uuid}::text, '{resource}', '{display} ' || COALESCE(d.{number}, ''),
				       'A line is discounted ' || round(m.max_pct, 1)::text || '%, well above the usual '
				         || round(u.avg_pct, 1)::text || '% for this workspace.',
				       `+ownerIdentity+`, `+involvedNone+`, d.{created}
				FROM {table} d
				JOIN lkp_record_status st ON st.record_status_id = d.{status}
				JOIN LATERAL (SELECT MAX(i.discount_percent) AS max_pct,
				                     COALESCE(SUM(i.discount_percent) FILTER (WHERE i.discount_percent > 0), 0) AS own_sum,
				                     COUNT(*) FILTER (WHERE i.discount_percent > 0) AS own_n
				              FROM {item} i
				              WHERE i.{fk} = d.{id} AND i.item_deleted_at IS NULL) m ON TRUE
				CROSS JOIN (SELECT COALESCE(SUM(discount_percent) FILTER (WHERE discount_percent > 0), 0) AS all_sum,
				                   COUNT(*) FILTER (WHERE discount_percent > 0) AS all_n
				            FROM {item} WHERE item_deleted_at IS NULL) b
				CROSS JOIN LATERAL (SELECT CASE WHEN b.all_n - m.own_n >= $8::int THEN (b.all_sum - m.own_sum) / (b.all_n - m.own_n) ELSE 0 END AS avg_pct) u
				`+ownerJoin("d", "{owner}")+`
				WHERE d.{deleted} IS NULL AND `+stNotIn("$4")+`
				  AND d.{created} >= $3::date - $7::int
				  AND m.max_pct >= LEAST($9::numeric, GREATEST($5::numeric, $6::numeric * u.avg_pct))`),
		},

		// A line sold for less than the item costs to buy.
		sqlRule{
			key: RuleBelowCostSale, severity: SeverityWarning, title: "%s sells below cost",
			audience: audience{owner: true, superAdmins: true},
			args:     []any{salesInactiveStatuses, lookbackDays},
			query: perDoc(soAndInvoice, `
				SELECT d.{uuid}::text, d.{uuid}::text, '{resource}', '{display} ' || COALESCE(d.{number}, ''),
				       b.n::text || ' line(s) are priced below the item''s purchase price, for example ' || b.sample || '.',
				       `+ownerIdentity+`, `+involvedNone+`, d.{created}
				FROM {table} d
				JOIN lkp_record_status st ON st.record_status_id = d.{status}
				JOIN LATERAL (
				    SELECT COUNT(*) AS n, MIN(i.item_name) AS sample
				    FROM {item} i JOIN inventory_item inv ON inv.inventory_item_id = i.inventory_item_id
				    WHERE i.{fk} = d.{id} AND i.item_deleted_at IS NULL
				      AND inv.inventory_item_purchase_unit_price > 0
				      AND i.unit_price < inv.inventory_item_purchase_unit_price) b ON b.n > 0
				`+ownerJoin("d", "{owner}")+`
				WHERE d.{deleted} IS NULL AND `+stNotIn("$4")+` AND d.{created} >= $3::date - $5::int`),
		},

		// An order far larger than the customer's own median.
		sqlRule{
			key: RuleUnusualOrderTotal, severity: SeverityWarning, title: "%s is far larger than this customer's usual order",
			audience: audience{owner: true, approvers: "sales_order"},
			args:     []any{minHistory, orderTotalMultiple, orderTotalFloor, lookbackDays},
			query: perDoc(salesOrder, `
				SELECT d.{uuid}::text, d.{uuid}::text, '{resource}', '{display} ' || COALESCE(d.{number}, ''),
				       'Total ' || d.{grand}::text || ' against a usual ' || round(h.med::numeric, 2)::text
				         || ' for this customer (median of ' || h.n::text || ' earlier orders).',
				       `+ownerIdentity+`, `+involvedNone+`, d.{created}
				FROM {table} d
				JOIN LATERAL (
				    SELECT COUNT(*) AS n, percentile_cont(0.5) WITHIN GROUP (ORDER BY o.{grand}) AS med
				    FROM {table} o JOIN lkp_record_status os ON os.record_status_id = o.{status}
				    WHERE o.{customer} = d.{customer} AND o.{id} < d.{id} AND o.{deleted} IS NULL
				      AND os.record_status_code <> 'CANC') h ON h.n >= $4::int AND h.med > 0
				`+ownerJoin("d", "{owner}")+`
				WHERE d.{deleted} IS NULL AND d.{created} >= $3::date - $7::int
				  AND d.{grand} >= $6::numeric AND d.{grand} > $5::numeric * h.med`),
		},

		// The same customer billed the same amount twice in a few days.
		sqlRule{
			key: RuleDuplicateInvoice, severity: SeverityWarning, title: "%s may be a duplicate invoice",
			audience: audience{owner: true, approvers: "invoice"},
			args:     []any{duplicateInvoiceDays, lookbackDays},
			query: perDoc(invoice, `
				SELECT d.{uuid}::text, d.{uuid}::text, '{resource}', '{display} ' || COALESCE(d.{number}, ''),
				       'Same customer and total as ' || COALESCE(dup.other, 'an earlier invoice') || ', dated within '
				         || $4::int::text || ' days. Check it before it is sent or paid.',
				       `+ownerIdentity+`, `+involvedNone+`, d.{created}
				FROM {table} d
				JOIN lkp_record_status st ON st.record_status_id = d.{status}
				JOIN LATERAL (
				    SELECT o.{number} AS other
				    FROM {table} o JOIN lkp_record_status os ON os.record_status_id = o.{status}
				    WHERE o.{customer} = d.{customer} AND o.{id} < d.{id} AND o.{deleted} IS NULL
				      AND os.record_status_code <> 'VOID' AND o.{grand} = d.{grand}
				      AND abs(o.{date} - d.{date}) <= $4::int
				    ORDER BY o.{id} LIMIT 1) dup ON TRUE
				`+ownerJoin("d", "{owner}")+`
				WHERE d.{deleted} IS NULL AND st.record_status_code <> 'VOID' AND d.{grand} > 0
				  AND d.{created} >= $3::date - $5::int`),
		},

		// Open invoices above the customer's credit limit, at most once a month.
		sqlRule{
			key: RuleOverCreditLimit, severity: SeverityWarning, title: "%s is over its credit limit",
			audience: audience{superAdmins: true, owner: true},
			args:     []any{invoiceUnpaidSkip},
			query: `
				SELECT c.customer_uuid::text || ':' || to_char($3::date, 'YYYY-MM'), c.customer_uuid::text, 'customer',
				       'Customer ' || c.customer_name,
				       'Open invoices total ' || o.open_total::text || ', over the credit limit of ' || c.customer_credit_limit::text || '.',
				       ` + ownerIdentity + `, ` + involvedNone + `, now()
				FROM customer c
				JOIN LATERAL (
				    SELECT COALESCE(SUM(i.invoice_balance_due), 0) AS open_total
				    FROM invoice i JOIN lkp_record_status st ON st.record_status_id = i.invoice_status
				    WHERE i.invoice_customer_id = c.customer_id AND i.invoice_deleted_at IS NULL
				      AND ` + stNotIn("$4") + `) o ON TRUE
				` + ownerJoin("c", "customer_sales_rep_user_id") + `
				WHERE c.customer_deleted_at IS NULL AND c.customer_credit_limit > 0
				  AND o.open_total > c.customer_credit_limit`,
		},

		// An invoice long past due. A state, not an event, so it is not limited to recent invoices.
		sqlRule{
			key: RuleOverdueInvoice, severity: SeverityWarning, title: "%s is well past due",
			audience: audience{owner: true, approvers: "invoice"},
			args:     []any{invoiceUnpaidSkip, overdueGraceDays, longLookbackDays},
			query: perDoc(invoice, `
				SELECT d.{uuid}::text, d.{uuid}::text, '{resource}', '{display} ' || COALESCE(d.{number}, ''),
				       ($3::date - d.invoice_due_date)::text || ' days past due with ' || d.invoice_balance_due::text || ' still owing.',
				       `+ownerIdentity+`, `+involvedNone+`, d.invoice_due_date::timestamp
				FROM {table} d
				JOIN lkp_record_status st ON st.record_status_id = d.{status}
				`+ownerJoin("d", "{owner}")+`
				WHERE d.{deleted} IS NULL AND `+stNotIn("$4")+` AND d.invoice_balance_due > 0
				  AND d.invoice_due_date < $3::date - $5::int AND d.invoice_due_date >= $3::date - $6::int`),
		},

		// Money received but never applied to an invoice.
		sqlRule{
			key: RuleStaleUnappliedPayment, severity: SeverityWarning, title: "%s has money that was never applied",
			audience: audience{owner: true, approvers: "payment"},
			args:     []any{staleUnappliedDays, longLookbackDays},
			query: `
				SELECT p.payment_uuid::text, p.payment_uuid::text, 'payment', 'Payment ' || COALESCE(p.payment_number, ''),
				       p.payment_unapplied_amount::text || ' of this payment has not been applied to any invoice for '
				         || ($3::date - p.payment_date)::text || ' days.',
				       ` + ownerIdentity + `, ` + involvedNone + `, p.payment_date::timestamp
				FROM payment p JOIN lkp_record_status st ON st.record_status_id = p.payment_status
				` + ownerJoin("p", "payment_owner_id") + `
				WHERE p.payment_deleted_at IS NULL AND st.record_status_code <> 'VOID' AND p.payment_unapplied_amount > 0
				  AND p.payment_date < $3::date - $4::int AND p.payment_date >= $3::date - $5::int`,
		},

		// A refund bigger than the payment it refunds.
		sqlRule{
			key: RuleRefundExceedsPayment, severity: SeverityCritical, title: "%s refunds more than was paid",
			audience: audience{owner: true, superAdmins: true},
			args:     []any{lookbackDays},
			query: `
				SELECT r.refund_uuid::text, r.refund_uuid::text, 'refund', 'Refund ' || COALESCE(r.refund_number, ''),
				       'Refund of ' || r.refund_amount::text || ' against a payment of ' || p.payment_amount::text
				         || ' (' || p.payment_refunded_total::text || ' refunded in total).',
				       ` + ownerIdentity + `, ` + involvedNone + `, r.refund_created_at
				FROM refund r
				JOIN payment p ON p.payment_id = r.refund_payment_id
				JOIN lkp_record_status st ON st.record_status_id = r.refund_status
				` + ownerJoin("r", "refund_owner_id") + `
				WHERE r.refund_deleted_at IS NULL AND st.record_status_code <> 'VOID'
				  AND r.refund_created_at >= $3::date - $4::int
				  AND (r.refund_amount > p.payment_amount OR p.payment_refunded_total > p.payment_amount)`,
		},

		// A sizeable refund the day after the money came in.
		sqlRule{
			key: RuleQuickRefund, severity: SeverityWarning, title: "%s was issued right after the payment",
			audience: audience{owner: true, superAdmins: true},
			args:     []any{quickRefundDays, quickRefundFloor, lookbackDays},
			query: `
				SELECT r.refund_uuid::text, r.refund_uuid::text, 'refund', 'Refund ' || COALESCE(r.refund_number, ''),
				       'Refund of ' || r.refund_amount::text || ' only ' || (r.refund_date - p.payment_date)::text
				         || ' day(s) after the payment it refunds.',
				       ` + ownerIdentity + `, ` + involvedNone + `, r.refund_created_at
				FROM refund r
				JOIN payment p ON p.payment_id = r.refund_payment_id
				JOIN lkp_record_status st ON st.record_status_id = r.refund_status
				` + ownerJoin("r", "refund_owner_id") + `
				WHERE r.refund_deleted_at IS NULL AND st.record_status_code <> 'VOID'
				  AND r.refund_amount >= $5::numeric AND r.refund_date - p.payment_date <= $4::int
				  AND r.refund_created_at >= $3::date - $6::int`,
		},

		// Several credit memos for one customer in a short time, at most once a month.
		sqlRule{
			key: RuleCreditMemoBurst, severity: SeverityWarning, title: "%s has had several credit memos recently",
			audience: audience{superAdmins: true},
			args:     []any{lookbackDays, creditMemoBurstCount},
			query: `
				SELECT c.customer_uuid::text || ':' || to_char($3::date, 'YYYY-MM'), c.customer_uuid::text, 'customer',
				       'Customer ' || c.customer_name,
				       g.n::text || ' credit memos totalling ' || g.total::text || ' in the last ' || $4::int::text || ' days.',
				       ` + involvedNone + `, ` + involvedNone + `, g.latest
				FROM (
				    SELECT m.credit_memo_customer_id AS customer_id, COUNT(*) AS n, SUM(m.credit_memo_grand_total) AS total,
				           MAX(m.credit_memo_created_at) AS latest
				    FROM credit_memo m JOIN lkp_record_status st ON st.record_status_id = m.credit_memo_status
				    WHERE m.credit_memo_deleted_at IS NULL AND st.record_status_code <> 'VOID'
				      AND m.credit_memo_created_at >= $3::date - $4::int
				    GROUP BY m.credit_memo_customer_id HAVING COUNT(*) >= $5::int) g
				JOIN customer c ON c.customer_id = g.customer_id`,
		},

		// An invoice or payment voided after it was already out the door.
		sqlRule{
			key: RuleVoidedAfterPosting, severity: SeverityWarning, title: "%s was voided after it was issued",
			audience: audience{superAdmins: true, owner: true},
			args:     []any{voidedLookbackDays},
			query: unionAll([]string{
				voidedQuery("invoice", "Invoice", "invoice_history", "invoice_history_id", "invoice_id", "invoice_uuid",
					"invoice_number", "invoice_owner_id", "INVC", "'SENT','PART','PAID','ODUE','APPV'"),
				voidedQuery("payment", "Payment", "payment_history", "payment_history_id", "payment_id", "payment_uuid",
					"payment_number", "payment_owner_id", "PYMT", "'APPV','DEPO'"),
			}),
		},

		// A document dated long before (or after) the day it was entered.
		sqlRule{
			key: RuleBackdatedDocument, severity: SeverityWarning, title: "%s has a date far from when it was entered",
			audience: audience{owner: true, superAdmins: true},
			args:     []any{backdateDays, futureDateDays, lookbackDays},
			query: unionAll([]string{
				backdateQuery("invoice", "Invoice", "invoice", "invoice_uuid", "invoice_number", "invoice_date", "invoice_created_at", "invoice_deleted_at", "invoice_owner_id"),
				backdateQuery("sales_order", "Sales Order", "sales_order", "sales_order_uuid", "sales_order_number", "sales_order_date", "sales_order_created_at", "sales_order_deleted_at", "sales_order_owner_id"),
				backdateQuery("payment", "Payment", "payment", "payment_uuid", "payment_number", "payment_date", "payment_created_at", "payment_deleted_at", "payment_owner_id"),
				backdateQuery("credit_memo", "Credit Memo", "credit_memo", "credit_memo_uuid", "credit_memo_number", "credit_memo_date", "credit_memo_created_at", "credit_memo_deleted_at", "credit_memo_owner_id"),
				backdateQuery("vendor_bill", "Vendor Bill", "vendor_bill", "vendor_bill_uuid", "vendor_bill_number", "vendor_bill_date", "vendor_bill_created_at", "vendor_bill_deleted_at", "vendor_bill_owner_id"),
				backdateQuery("purchase_order", "Purchase Order", "purchase_order", "purchase_order_uuid", "purchase_order_number", "purchase_order_date", "purchase_order_created_at", "purchase_order_deleted_at", "purchase_order_owner_id"),
			}),
		},

		// An order whose total moved a lot when the quote was converted.
		sqlRule{
			key: RulePriceDriftOnConvert, severity: SeverityWarning, title: "%s differs a lot from the quote it came from",
			audience: audience{owner: true, approvers: "sales_order"},
			args:     []any{conversionDriftPercent, lookbackDays},
			query: `
				SELECT so.sales_order_uuid::text, so.sales_order_uuid::text, 'sales_order', 'Sales Order ' || COALESCE(so.sales_order_number, ''),
				       'Quote ' || COALESCE(q.quote_number, '') || ' was ' || q.quote_grand_total::text || ' but the order is '
				         || so.sales_order_grand_total::text || '.',
				       ` + ownerIdentity + `, ` + involvedNone + `, qc.converted_at
				FROM quote_conversion qc
				JOIN quote q ON q.quote_id = qc.quote_id
				JOIN sales_order so ON so.sales_order_id = qc.sales_order_id
				` + ownerJoin("so", "sales_order_owner_id") + `
				WHERE so.sales_order_deleted_at IS NULL AND q.quote_grand_total > 0
				  AND qc.converted_at >= $3::date - $5::int
				  AND abs(so.sales_order_grand_total - q.quote_grand_total) > q.quote_grand_total * $4::numeric / 100`,
		},

		// No tax on an invoice for a customer who normally pays it.
		sqlRule{
			key: RuleZeroTaxUnusual, severity: SeverityWarning, title: "%s has no sales tax",
			audience: audience{owner: true, approvers: "invoice"},
			args:     []any{zeroTaxMinHistory, lookbackDays},
			query: `
				SELECT d.invoice_uuid::text, d.invoice_uuid::text, 'invoice', 'Invoice ' || COALESCE(d.invoice_number, ''),
				       'No tax was charged, but ' || h.n::text || ' earlier invoices for this customer were taxed and the customer is not tax exempt.',
				       ` + ownerIdentity + `, ` + involvedNone + `, d.invoice_created_at
				FROM invoice d
				JOIN customer c ON c.customer_id = d.invoice_customer_id
				JOIN lkp_record_status st ON st.record_status_id = d.invoice_status
				JOIN LATERAL (SELECT COUNT(*) AS n FROM invoice o
				              WHERE o.invoice_customer_id = d.invoice_customer_id AND o.invoice_id < d.invoice_id
				                AND o.invoice_deleted_at IS NULL AND o.invoice_tax_total > 0) h ON h.n >= $4::int
				` + ownerJoin("d", "invoice_owner_id") + `
				WHERE d.invoice_deleted_at IS NULL AND st.record_status_code <> 'VOID'
				  AND d.invoice_tax_total = 0 AND d.invoice_subtotal > 0 AND c.customer_is_tax_exempt = FALSE
				  AND d.invoice_created_at >= $3::date - $5::int`,
		},

		// A sales order that was filled but never invoiced.
		sqlRule{
			key: RuleFilledNotInvoiced, severity: SeverityWarning, title: "%s was filled but never invoiced",
			audience: audience{owner: true, approvers: "sales_order"},
			args:     []any{notInvoicedDays, longLookbackDays},
			query: `
				SELECT so.sales_order_uuid::text, so.sales_order_uuid::text, 'sales_order', 'Sales Order ' || COALESCE(so.sales_order_number, ''),
				       'Filled on ' || to_char(f.filled_at, 'YYYY-MM-DD') || ' with no invoice since.',
				       ` + ownerIdentity + `, ` + involvedNone + `, f.filled_at
				FROM sales_order so
				JOIN lkp_record_status st ON st.record_status_id = so.sales_order_status AND st.record_status_code = 'FILL'
				JOIN LATERAL (
				    SELECT MAX(h.at) AS filled_at FROM sales_order_history h
				    WHERE h.sales_order_id = so.sales_order_id AND h.to_status_id = so.sales_order_status) f ON f.filled_at IS NOT NULL
				` + ownerJoin("so", "sales_order_owner_id") + `
				WHERE so.sales_order_deleted_at IS NULL
				  AND f.filled_at < $3::date - $4::int AND f.filled_at >= $3::date - $5::int
				  AND NOT EXISTS (
				      SELECT 1 FROM invoice i JOIN lkp_record_status ist ON ist.record_status_id = i.invoice_status
				      WHERE i.invoice_sales_order_id = so.sales_order_id AND i.invoice_deleted_at IS NULL
				        AND ist.record_status_code <> 'VOID')`,
		},
	}
}

// voidedQuery selects documents moved to VOID from an already-issued status
// within the last voidedLookbackDays ($4). The person who voided it is the
// involved identity.
func voidedQuery(resource, display, historyTable, historyID, fk, uuid, number, owner, typeCode, fromCodes string) string {
	return tpl(`
		SELECT '{resource}-void:' || h.{hid}::text, d.{uuid}::text, '{resource}', '{display} ' || COALESCE(d.{number}, ''),
		       'Voided on ' || to_char(h.at, 'YYYY-MM-DD') || ' after it had been issued.',
		       `+ownerIdentity+`, ua.identity_id::text, h.at
		FROM {history} h
		JOIN {resource} d ON d.{fk} = h.{fk}
		JOIN lkp_record_type rt ON rt.record_type_code = '{type}'
		JOIN lkp_record_status fs ON fs.record_status_id = h.from_status_id AND fs.record_status_record_type = rt.record_type_id
		JOIN lkp_record_status ts ON ts.record_status_id = h.to_status_id AND ts.record_status_record_type = rt.record_type_id
		`+ownerJoin("d", "{owner}")+`
		LEFT JOIN employee ea ON ea.employee_id = h.actor_employee_id
		LEFT JOIN users ua ON ua.id = ea.employee_user_id
		WHERE ts.record_status_code = 'VOID' AND fs.record_status_code IN ({from})
		  AND h.at >= $3::date - $4::int`,
		"{resource}", resource, "{display}", display, "{history}", historyTable, "{hid}", historyID,
		"{fk}", fk, "{uuid}", uuid, "{number}", number, "{owner}", owner, "{type}", typeCode, "{from}", fromCodes)
}

// backdateQuery selects documents whose own date is more than backdateDays ($4)
// before, or futureDateDays ($5) after, the day they were created, among those
// created within the last lookbackDays ($6).
func backdateQuery(resource, display, table, uuid, number, date, created, deleted, owner string) string {
	return tpl(`
		SELECT '{resource}:' || d.{uuid}::text, d.{uuid}::text, '{resource}', '{display} ' || COALESCE(d.{number}, ''),
		       CASE WHEN d.{date} < d.{created}::date
		            THEN 'Dated ' || (d.{created}::date - d.{date})::text || ' days before it was entered.'
		            ELSE 'Dated ' || (d.{date} - d.{created}::date)::text || ' days after it was entered.' END,
		       `+ownerIdentity+`, `+involvedNone+`, d.{created}
		FROM {table} d
		`+ownerJoin("d", "{owner}")+`
		WHERE d.{deleted} IS NULL AND d.{created} >= $3::date - $6::int
		  AND (d.{date} < d.{created}::date - $4::int OR d.{date} > d.{created}::date + $5::int)`,
		"{resource}", resource, "{display}", display, "{table}", table, "{uuid}", uuid, "{number}", number,
		"{date}", date, "{created}", created, "{deleted}", deleted, "{owner}", owner)
}
