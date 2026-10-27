package anomaly

// Rule keys for the purchasing and money-out rules, stored in anomaly_rule_key.
const (
	RuleBillExceedsPO         = "bill_exceeds_po"
	RuleVendorOverpaid        = "vendor_overpaid"
	RuleNewVendorLargePayment = "new_vendor_large_payment"
	RulePOPriceAboveCost      = "po_price_above_cost"
	RuleSplitPurchaseOrders   = "split_purchase_orders"
	RulePOLate                = "po_late"
	RulePOPartialStuck        = "po_partial_stuck"
	RuleExpenseDuplicate      = "expense_duplicate"
	RuleExpenseUnusuallyHigh  = "expense_unusually_high"
)

var (
	poSkipStatuses      = []string{"CANC"}
	poSplitSkipStatuses = []string{"CANC", "DRFT"}
)

func purchasingRules() []Rule {
	return []Rule{
		// Bills against a PO add up to more than the PO.
		sqlRule{
			key: RuleBillExceedsPO, severity: SeverityWarning, title: "Bills for %s add up to more than the order",
			audience: audience{owner: true, approvers: "vendor_bill"},
			args:     []any{poBillTolerancePercent, poBillToleranceAmount, longLookbackDays},
			query: `
				SELECT p.purchase_order_uuid::text, p.purchase_order_uuid::text, 'purchase_order',
				       'Purchase Order ' || COALESCE(p.purchase_order_number, ''),
				       'Bills against this order total ' || b.billed::text || ' but the order is only '
				         || p.purchase_order_grand_total::text || '.',
				       ` + ownerIdentity + `, ` + involvedNone + `, p.purchase_order_created_at
				FROM purchase_order p
				JOIN LATERAL (
				    SELECT COALESCE(SUM(vb.vendor_bill_grand_total), 0) AS billed
				    FROM vendor_bill vb JOIN lkp_record_status st ON st.record_status_id = vb.vendor_bill_status
				    WHERE vb.vendor_bill_purchase_order_id = p.purchase_order_id
				      AND vb.vendor_bill_deleted_at IS NULL AND st.record_status_code <> 'VOID') b ON TRUE
				` + ownerJoin("p", "purchase_order_owner_id") + `
				WHERE p.purchase_order_deleted_at IS NULL AND p.purchase_order_created_at >= $3::date - $6::int
				  AND b.billed > p.purchase_order_grand_total * (1 + $4::numeric / 100) + $5::numeric`,
		},

		// More applied to a bill than the bill is for. (A payment applying more than
		// its own amount cannot exist: the database refuses it.)
		sqlRule{
			key: RuleVendorOverpaid, severity: SeverityCritical, title: "%s has been paid more than it is worth",
			audience: audience{owner: true, superAdmins: true},
			args:     []any{moneyEpsilon, lookbackDays},
			query: `
				SELECT vb.vendor_bill_uuid::text, vb.vendor_bill_uuid::text, 'vendor_bill',
				       'Vendor Bill ' || COALESCE(vb.vendor_bill_number, ''),
				       'Payments applied total ' || a.applied::text || ' against a bill of ' || vb.vendor_bill_grand_total::text || '.',
				       ` + ownerIdentity + `, ` + involvedNone + `, vb.vendor_bill_created_at
				FROM vendor_bill vb
				JOIN LATERAL (SELECT COALESCE(SUM(x.application_amount), 0) AS applied FROM vendor_payment_application x
				              WHERE x.vendor_bill_id = vb.vendor_bill_id AND x.application_deleted_at IS NULL) a ON TRUE
				` + ownerJoin("vb", "vendor_bill_owner_id") + `
				WHERE vb.vendor_bill_deleted_at IS NULL AND vb.vendor_bill_created_at >= $3::date - $5::int
				  AND a.applied > vb.vendor_bill_grand_total + $4::numeric`,
		},

		// A big payment to a vendor that was only just created.
		sqlRule{
			key: RuleNewVendorLargePayment, severity: SeverityWarning, title: "%s goes to a very new vendor",
			audience: audience{superAdmins: true, approvers: "vendor_payment"},
			args:     []any{newVendorDays, newVendorPaymentFloor, lookbackDays},
			query: `
				SELECT vp.vendor_payment_uuid::text, vp.vendor_payment_uuid::text, 'vendor_payment',
				       'Vendor Payment ' || COALESCE(vp.vendor_payment_number, ''),
				       'A payment of ' || vp.vendor_payment_amount::text || ' to a vendor created only '
				         || (vp.vendor_payment_created_at::date - v.vendor_created_at::date)::text || ' days earlier.',
				       ` + ownerIdentity + `, ` + involvedNone + `, vp.vendor_payment_created_at
				FROM vendor_payment vp
				JOIN vendor v ON v.vendor_id = vp.vendor_payment_vendor_id
				JOIN lkp_record_status st ON st.record_status_id = vp.vendor_payment_status
				` + ownerJoin("vp", "vendor_payment_owner_id") + `
				WHERE vp.vendor_payment_deleted_at IS NULL AND st.record_status_code IN ('APPV', 'SENT', 'SCHD')
				  AND vp.vendor_payment_amount >= $5::numeric
				  AND v.vendor_created_at >= vp.vendor_payment_created_at - ($4::int * interval '1 day')
				  AND vp.vendor_payment_created_at >= $3::date - $6::int`,
		},

		// A PO line priced well above what the item normally costs.
		sqlRule{
			key: RulePOPriceAboveCost, severity: SeverityWarning, title: "%s is priced well above the usual cost",
			audience: audience{owner: true, approvers: "purchase_order"},
			args:     []any{poSkipStatuses, poPriceMultiple, lookbackDays},
			query: `
				SELECT p.purchase_order_uuid::text, p.purchase_order_uuid::text, 'purchase_order',
				       'Purchase Order ' || COALESCE(p.purchase_order_number, ''),
				       b.n::text || ' line(s) cost more than ' || $5::numeric::text || ' times the item''s usual purchase price, for example '
				         || b.sample || '.',
				       ` + ownerIdentity + `, ` + involvedNone + `, p.purchase_order_created_at
				FROM purchase_order p
				JOIN lkp_record_status st ON st.record_status_id = p.purchase_order_status
				JOIN LATERAL (
				    SELECT COUNT(*) AS n, MIN(i.item_name) AS sample
				    FROM purchase_order_item i JOIN inventory_item inv ON inv.inventory_item_id = i.inventory_item_id
				    WHERE i.purchase_order_id = p.purchase_order_id AND i.item_deleted_at IS NULL
				      AND inv.inventory_item_purchase_unit_price > 0
				      AND i.unit_price > inv.inventory_item_purchase_unit_price * $5::numeric) b ON b.n > 0
				` + ownerJoin("p", "purchase_order_owner_id") + `
				WHERE p.purchase_order_deleted_at IS NULL AND ` + stNotIn("$4") + ` AND p.purchase_order_created_at >= $3::date - $6::int`,
		},

		// Several POs to one vendor from one person in a single day (a way round approval).
		sqlRule{
			key: RuleSplitPurchaseOrders, severity: SeverityWarning, title: "%s was split into several orders in one day",
			audience: audience{superAdmins: true, approvers: "purchase_order"},
			args:     []any{poSplitSkipStatuses, lookbackDays, splitPOCount},
			query: `
				SELECT v.vendor_uuid::text || ':' || COALESCE(g.owner_id::text, '-') || ':' || g.day::text, v.vendor_uuid::text, 'purchase_order',
				       'Vendor ' || COALESCE(g.vendor_name, ''),
				       g.n::text || ' purchase orders totalling ' || g.total::text || ' were raised for this vendor on ' || g.day::text || '.',
				       ` + involvedNone + `, uo.identity_id::text, g.latest
				FROM (
				    SELECT p.purchase_order_vendor_id AS vendor_id, p.purchase_order_owner_id AS owner_id,
				           p.purchase_order_created_at::date AS day, COUNT(*) AS n, SUM(p.purchase_order_grand_total) AS total,
				           MAX(p.purchase_order_vendor_name) AS vendor_name, MAX(p.purchase_order_created_at) AS latest
				    FROM purchase_order p JOIN lkp_record_status st ON st.record_status_id = p.purchase_order_status
				    WHERE p.purchase_order_deleted_at IS NULL AND ` + stNotIn("$4") + `
				      AND p.purchase_order_created_at >= $3::date - $5::int
				    GROUP BY 1, 2, 3 HAVING COUNT(*) >= $6::int) g
				JOIN vendor v ON v.vendor_id = g.vendor_id
				LEFT JOIN employee eo ON eo.employee_id = g.owner_id
				LEFT JOIN users uo ON uo.id = eo.employee_user_id`,
		},

		// An open PO past its expected delivery date.
		sqlRule{
			key: RulePOLate, severity: SeverityWarning, title: "%s is overdue for delivery",
			audience: audience{owner: true, approvers: "purchase_order"},
			args:     []any{poLateGraceDays, longLookbackDays},
			query: `
				SELECT p.purchase_order_uuid::text, p.purchase_order_uuid::text, 'purchase_order',
				       'Purchase Order ' || COALESCE(p.purchase_order_number, ''),
				       'Expected on ' || to_char(p.purchase_order_expected_date, 'YYYY-MM-DD') || ' ('
				         || ($3::date - p.purchase_order_expected_date)::text || ' days ago) and not fully received.',
				       ` + ownerIdentity + `, ` + involvedNone + `, p.purchase_order_expected_date::timestamp
				FROM purchase_order p JOIN lkp_record_status st ON st.record_status_id = p.purchase_order_status
				` + ownerJoin("p", "purchase_order_owner_id") + `
				WHERE p.purchase_order_deleted_at IS NULL AND st.record_status_code IN ('APPV', 'SENT', 'PART')
				  AND p.purchase_order_expected_date < $3::date - $4::int
				  AND p.purchase_order_expected_date >= $3::date - $5::int`,
		},

		// A partly received PO that nothing has arrived for in a month.
		sqlRule{
			key: RulePOPartialStuck, severity: SeverityWarning, title: "%s is only partly received and has stalled",
			audience: audience{owner: true, approvers: "purchase_order"},
			args:     []any{poPartialStuckDays, longLookbackDays},
			query: `
				SELECT p.purchase_order_uuid::text, p.purchase_order_uuid::text, 'purchase_order',
				       'Purchase Order ' || COALESCE(p.purchase_order_number, ''),
				       'Partly received, with no receipt in the last ' || $4::int::text || ' days.',
				       ` + ownerIdentity + `, ` + involvedNone + `, p.purchase_order_created_at
				FROM purchase_order p JOIN lkp_record_status st ON st.record_status_id = p.purchase_order_status
				` + ownerJoin("p", "purchase_order_owner_id") + `
				WHERE p.purchase_order_deleted_at IS NULL AND st.record_status_code = 'PART'
				  AND p.purchase_order_created_at >= $3::date - $5::int
				  AND NOT EXISTS (
				      SELECT 1 FROM item_receipt ir
				      WHERE ir.purchase_order_id = p.purchase_order_id AND ir.item_receipt_deleted_at IS NULL
				        AND ir.item_receipt_date >= $3::date - $4::int)`,
		},

		// An expense that repeats an earlier one from the same person.
		sqlRule{
			key: RuleExpenseDuplicate, severity: SeverityWarning, title: "%s repeats an earlier expense",
			audience: audience{superAdmins: true, approvers: "expense"},
			args:     []any{lookbackDays},
			query: `
				SELECT e.expense_uuid::text, e.expense_uuid::text, 'expense', 'Expense ' || COALESCE(e.expense_number, ''),
				       'A line matches an earlier expense from the same person: same amount, date and category.',
				       ` + involvedNone + `, uc.identity_id::text, e.expense_created_at
				FROM expense e
				JOIN lkp_record_status st ON st.record_status_id = e.expense_status
				LEFT JOIN employee ec ON ec.employee_id = e.expense_claimant_id
				LEFT JOIN users uc ON uc.id = ec.employee_user_id
				WHERE e.expense_deleted_at IS NULL AND st.record_status_code <> 'RJCT'
				  AND e.expense_created_at >= $3::date - $4::int
				  AND EXISTS (
				      SELECT 1
				      FROM expense_item ei
				      JOIN expense_item oi ON oi.amount = ei.amount AND oi.expense_date = ei.expense_date
				                          AND oi.category_id IS NOT DISTINCT FROM ei.category_id AND oi.item_deleted_at IS NULL
				      JOIN expense oe ON oe.expense_id = oi.expense_id AND oe.expense_id < e.expense_id
				                     AND oe.expense_claimant_id = e.expense_claimant_id AND oe.expense_deleted_at IS NULL
				      JOIN lkp_record_status ost ON ost.record_status_id = oe.expense_status AND ost.record_status_code <> 'RJCT'
				      WHERE ei.expense_id = e.expense_id AND ei.item_deleted_at IS NULL)`,
		},

		// An expense line far above what that category usually costs.
		sqlRule{
			key: RuleExpenseUnusuallyHigh, severity: SeverityWarning, title: "A line on %s is far above the usual for its category",
			audience: audience{superAdmins: true, approvers: "expense"},
			args:     []any{expenseHighFloor, expenseMinHistory, expenseHighMultiple, lookbackDays},
			query: `
				SELECT ei.expense_item_uuid::text, e.expense_uuid::text, 'expense', 'Expense ' || COALESCE(e.expense_number, ''),
				       'A line of ' || ei.amount::text || ' against a usual ' || round(h.med::numeric, 2)::text
				         || ' for this category (median of ' || h.n::text || ' earlier lines).',
				       ` + involvedNone + `, uc.identity_id::text, e.expense_created_at
				FROM expense_item ei
				JOIN expense e ON e.expense_id = ei.expense_id
				JOIN lkp_record_status st ON st.record_status_id = e.expense_status
				JOIN LATERAL (
				    SELECT COUNT(*) AS n, percentile_cont(0.5) WITHIN GROUP (ORDER BY oi.amount) AS med
				    FROM expense_item oi JOIN expense oe ON oe.expense_id = oi.expense_id
				    WHERE oi.category_id IS NOT DISTINCT FROM ei.category_id AND oi.expense_item_id < ei.expense_item_id
				      AND oi.item_deleted_at IS NULL AND oe.expense_deleted_at IS NULL) h ON h.n >= $5::int AND h.med > 0
				LEFT JOIN employee ec ON ec.employee_id = e.expense_claimant_id
				LEFT JOIN users uc ON uc.id = ec.employee_user_id
				WHERE ei.item_deleted_at IS NULL AND e.expense_deleted_at IS NULL AND st.record_status_code <> 'RJCT'
				  AND ei.amount >= $4::numeric AND ei.amount > $6::numeric * h.med
				  AND e.expense_created_at >= $3::date - $7::int`,
		},
	}
}
