package anomaly

// Rule keys for the inventory rules, stored in anomaly_rule_key.
const (
	RuleOverAllocated        = "over_allocated"
	RuleStockLedgerMismatch  = "stock_ledger_mismatch"
	RuleLargeWriteOff        = "large_write_off"
	RuleAdjustmentBurst      = "adjustment_burst"
	RuleCountVarianceHigh    = "count_variance_high"
	RuleReservedForClosed    = "reserved_for_closed_order"
	RuleDeadStock            = "dead_stock"
	RuleBinTransferStuck     = "bin_transfer_stuck"
	RuleReceivedSlabMismatch = "received_slab_mismatch"
	RuleShrinkageSpike       = "shrinkage_spike"
)

func inventoryRules() []Rule {
	return []Rule{
		// More stock promised to orders than is on hand. (A negative balance itself
		// cannot exist: the database refuses it.)
		sqlRule{
			key: RuleOverAllocated, severity: SeverityWarning, title: "%s is promised to more orders than is in stock",
			audience: audience{superAdmins: true},
			query: `
				SELECT inv.inventory_item_uuid::text || ':' || al.warehouse_id::text || ':' || to_char($3::date, 'YYYY-MM'),
				       inv.inventory_item_uuid::text, 'inventory_item', 'Item ' || inv.inventory_item_name,
				       al.reserved::text || ' is reserved for orders but only ' || COALESCE(s.quantity_on_hand, 0)::text || ' is on hand.',
				       ` + involvedNone + `, ` + involvedNone + `, now()
				FROM (
				    SELECT a.inventory_item_id, a.warehouse_id, SUM(a.allocated_quantity - a.fulfilled_quantity) AS reserved
				    FROM inventory_allocation a WHERE a.allocation_status = 'reserved'
				    GROUP BY a.inventory_item_id, a.warehouse_id) al
				JOIN inventory_item inv ON inv.inventory_item_id = al.inventory_item_id
				     AND inv.inventory_item_track_stock AND inv.inventory_item_deleted_at IS NULL
				LEFT JOIN inventory_stock s ON s.inventory_item_id = al.inventory_item_id AND s.warehouse_id = al.warehouse_id
				WHERE al.reserved > COALESCE(s.quantity_on_hand, 0)`,
		},

		// The stock balance disagrees with the sum of its own movement history.
		sqlRule{
			key: RuleStockLedgerMismatch, severity: SeverityWarning, title: "%s does not match its movement history",
			audience: audience{superAdmins: true},
			query: `
				SELECT inv.inventory_item_uuid::text || ':' || s.warehouse_id::text || ':' || to_char($3::date, 'YYYY-MM'),
				       inv.inventory_item_uuid::text, 'inventory_item', 'Item ' || inv.inventory_item_name,
				       'The balance says ' || s.quantity_on_hand::text || ' but every recorded movement adds up to ' || l.net::text || '.',
				       ` + involvedNone + `, ` + involvedNone + `, s.stock_updated_at
				FROM inventory_stock s
				JOIN (SELECT inventory_item_id, warehouse_id, SUM(quantity_delta) AS net
				      FROM inventory_ledger GROUP BY inventory_item_id, warehouse_id) l
				     ON l.inventory_item_id = s.inventory_item_id AND l.warehouse_id = s.warehouse_id
				JOIN inventory_item inv ON inv.inventory_item_id = s.inventory_item_id AND inv.inventory_item_deleted_at IS NULL
				WHERE s.quantity_on_hand <> l.net`,
		},

		// A posted adjustment that wrote off a lot of stock value.
		sqlRule{
			key: RuleLargeWriteOff, severity: SeverityWarning, title: "%s wrote off a large amount of stock",
			audience: audience{superAdmins: true, owner: true},
			args:     []any{writeOffFloor, lookbackDays},
			query: `
				SELECT ad.inventory_adjustment_uuid::text, ad.inventory_adjustment_uuid::text, 'inventory_adjustment',
				       'Adjustment ' || COALESCE(ad.adjustment_number, ''),
				       'Wrote off stock worth about ' || round(w.value, 2)::text || ' at purchase price.',
				       ` + ownerIdentity + `, ` + involvedNone + `, ad.adjustment_posted_at
				FROM inventory_adjustment ad
				JOIN lkp_record_status st ON st.record_status_id = ad.adjustment_status AND st.record_status_code = 'POST'
				JOIN LATERAL (
				    SELECT COALESCE(SUM(-l.qty_delta * COALESCE(inv.inventory_item_purchase_unit_price, 0)), 0) AS value
				    FROM inventory_adjustment_line l JOIN inventory_item inv ON inv.inventory_item_id = l.inventory_item_id
				    WHERE l.inventory_adjustment_id = ad.inventory_adjustment_id AND l.line_deleted_at IS NULL AND l.qty_delta < 0) w
				     ON w.value >= $4::numeric
				` + ownerJoin("ad", "adjustment_owner_id") + `
				WHERE ad.adjustment_deleted_at IS NULL AND ad.adjustment_posted_at >= $3::date - $5::int`,
		},

		// One person posting many adjustments in a short time, at most once a week.
		sqlRule{
			key: RuleAdjustmentBurst, severity: SeverityWarning, title: "%s has been posting many stock adjustments",
			audience: audience{superAdmins: true},
			args:     []any{adjustmentBurstDays, adjustmentBurstCount},
			query: `
				SELECT u.identity_id::text || ':' || to_char($3::date, 'IYYY-IW'), NULL::text, 'inventory_adjustment',
				       'The user ' || COALESCE(NULLIF(u.full_name, ''), u.email),
				       g.n::text || ' stock adjustments were posted in the last ' || $4::int::text || ' days.',
				       ` + involvedNone + `, u.identity_id::text, g.latest
				FROM (
				    SELECT ad.adjustment_owner_id AS owner_id, COUNT(*) AS n, MAX(ad.adjustment_posted_at) AS latest
				    FROM inventory_adjustment ad
				    JOIN lkp_record_status st ON st.record_status_id = ad.adjustment_status AND st.record_status_code = 'POST'
				    WHERE ad.adjustment_deleted_at IS NULL AND ad.adjustment_posted_at >= $3::date - $4::int
				    GROUP BY ad.adjustment_owner_id HAVING COUNT(*) >= $5::int) g
				JOIN employee e ON e.employee_id = g.owner_id
				JOIN users u ON u.id = e.employee_user_id`,
		},

		// A stock count where counted lines are well off the system quantity.
		sqlRule{
			key: RuleCountVarianceHigh, severity: SeverityWarning, title: "%s found large differences from the system",
			audience: audience{superAdmins: true, owner: true},
			args:     []any{countVariancePercent, lookbackDays},
			query: `
				SELECT c.inventory_count_uuid::text, c.inventory_count_uuid::text, 'inventory_count',
				       'Stock count ' || COALESCE(c.count_number, ''),
				       b.n::text || ' line(s) are off by ' || $4::numeric::text || '% or more; the biggest difference is '
				         || b.max_variance::text || '.',
				       ` + ownerIdentity + `, ` + involvedNone + `, c.count_created_at
				FROM inventory_count c
				JOIN lkp_record_status st ON st.record_status_id = c.count_status AND st.record_status_code IN ('RVW_', 'APPV', 'POST')
				JOIN LATERAL (
				    SELECT COUNT(*) AS n, MAX(abs(l.count_variance)) AS max_variance
				    FROM inventory_count_line l
				    WHERE l.inventory_count_id = c.inventory_count_id AND l.line_deleted_at IS NULL AND l.system_qty > 0
				      AND abs(l.count_variance) * 100 >= $4::numeric * l.system_qty) b ON b.n > 0
				` + ownerJoin("c", "count_owner_id") + `
				WHERE c.count_deleted_at IS NULL AND c.count_created_at >= $3::date - $5::int`,
		},

		// Stock still reserved for an order that was cancelled.
		sqlRule{
			key: RuleReservedForClosed, severity: SeverityWarning, title: "Stock is still reserved for cancelled %s",
			audience: audience{owner: true, superAdmins: true},
			query: `
				SELECT a.inventory_allocation_uuid::text, so.sales_order_uuid::text, 'sales_order',
				       'Sales Order ' || COALESCE(so.sales_order_number, ''),
				       (a.allocated_quantity - a.fulfilled_quantity)::text || ' of ' || inv.inventory_item_name
				         || ' is still reserved for this cancelled order.',
				       ` + ownerIdentity + `, ` + involvedNone + `, a.allocation_updated_at
				FROM inventory_allocation a
				JOIN sales_order so ON so.sales_order_id = a.sales_order_id
				JOIN lkp_record_status st ON st.record_status_id = so.sales_order_status AND st.record_status_code = 'CANC'
				JOIN inventory_item inv ON inv.inventory_item_id = a.inventory_item_id
				` + ownerJoin("so", "sales_order_owner_id") + `
				WHERE a.allocation_status = 'reserved' AND a.allocated_quantity > a.fulfilled_quantity`,
		},

		// Slabs nobody has touched for months. Information only: it appears on the page, not in the bell.
		sqlRule{
			key: RuleDeadStock, severity: SeverityInfo, title: "%s has slabs that have not moved in months",
			audience: audience{superAdmins: true},
			args:     []any{deadStockDays},
			query: `
				SELECT inv.inventory_item_uuid::text || ':' || g.warehouse_id::text || ':' || to_char($3::date, 'YYYY-"Q"Q'),
				       inv.inventory_item_uuid::text, 'inventory_item', 'Item ' || inv.inventory_item_name,
				       g.n::text || ' available slab(s) have had no movement for ' || $4::int::text || ' days.',
				       ` + involvedNone + `, ` + involvedNone + `, now()
				FROM (
				    SELECT s.inventory_item_id, s.warehouse_id, COUNT(*) AS n
				    FROM inventory_slab s
				    WHERE s.slab_status = 'available' AND s.slab_deleted_at IS NULL
				      AND COALESCE(s.slab_received_at::timestamp, s.slab_created_at) < $3::date - $4::int
				      AND NOT EXISTS (SELECT 1 FROM inventory_slab_ledger l
				                      WHERE l.inventory_slab_id = s.inventory_slab_id AND l.occurred_at >= $3::date - $4::int)
				    GROUP BY s.inventory_item_id, s.warehouse_id) g
				JOIN inventory_item inv ON inv.inventory_item_id = g.inventory_item_id AND inv.inventory_item_deleted_at IS NULL`,
		},

		// A bin transfer left open for too long.
		sqlRule{
			key: RuleBinTransferStuck, severity: SeverityWarning, title: "%s has been waiting too long",
			audience: audience{owner: true, approvers: "bin_transfer"},
			args:     []any{binTransferStuckDays, longLookbackDays},
			query: `
				SELECT t.bin_transfer_uuid::text, t.bin_transfer_uuid::text, 'bin_transfer',
				       'Bin transfer ' || COALESCE(t.bin_transfer_number, ''),
				       'Still ' || lower(st.record_status_name) || ' after ' || ($3::date - t.bin_transfer_created_at::date)::text || ' days.',
				       ` + ownerIdentity + `, ` + involvedNone + `, t.bin_transfer_created_at
				FROM bin_transfer t
				JOIN lkp_record_status st ON st.record_status_id = t.bin_transfer_status AND st.record_status_code IN ('PAPV', 'APPV')
				` + ownerJoin("t", "bin_transfer_owner_id") + `
				WHERE t.bin_transfer_deleted_at IS NULL
				  AND t.bin_transfer_created_at < $3::date - $4::int AND t.bin_transfer_created_at >= $3::date - $5::int`,
		},

		// A received PO whose slab count differs from what the order expected.
		sqlRule{
			key: RuleReceivedSlabMismatch, severity: SeverityWarning, title: "%s was received with a different slab count",
			audience: audience{owner: true, approvers: "purchase_order"},
			args:     []any{longLookbackDays},
			query: `
				SELECT poi.purchase_order_item_uuid::text, p.purchase_order_uuid::text, 'purchase_order',
				       'Purchase Order ' || COALESCE(p.purchase_order_number, ''),
				       poi.item_name || ': ' || poi.expected_slabs::text || ' slab(s) expected but ' || r.received::text || ' received.',
				       ` + ownerIdentity + `, ` + involvedNone + `, p.purchase_order_created_at
				FROM purchase_order_item poi
				JOIN purchase_order p ON p.purchase_order_id = poi.purchase_order_id
				JOIN lkp_record_status st ON st.record_status_id = p.purchase_order_status AND st.record_status_code IN ('RCVD', 'CLSD')
				JOIN LATERAL (
				    SELECT COUNT(u.item_receipt_line_unit_id) AS received
				    FROM item_receipt_line irl
				    JOIN item_receipt ir ON ir.item_receipt_id = irl.item_receipt_id
				    JOIN lkp_record_status rst ON rst.record_status_id = ir.item_receipt_status AND rst.record_status_code <> 'VOID'
				    JOIN item_receipt_line_unit u ON u.item_receipt_line_id = irl.item_receipt_line_id
				    WHERE irl.purchase_order_item_id = poi.purchase_order_item_id AND irl.item_deleted_at IS NULL) r ON TRUE
				` + ownerJoin("p", "purchase_order_owner_id") + `
				WHERE poi.item_deleted_at IS NULL AND p.purchase_order_deleted_at IS NULL
				  AND poi.expected_slabs > 0 AND r.received <> poi.expected_slabs
				  AND p.purchase_order_created_at >= $3::date - $4::int`,
		},

		// Write-offs at one location jumping compared with the period before, at most once a month.
		sqlRule{
			key: RuleShrinkageSpike, severity: SeverityWarning, title: "%s is losing much more stock than before",
			audience: audience{superAdmins: true},
			args:     []any{shrinkagePeriodDays, shrinkageFloor, shrinkageMultiple},
			query: `
				SELECT loc.company_location_uuid::text || ':' || to_char($3::date, 'YYYY-MM'), loc.company_location_uuid::text,
				       'warehouse', 'Location ' || loc.name,
				       g.recent::text || ' units written off in the last ' || $4::int::text || ' days, against '
				         || COALESCE(g.prior, 0)::text || ' in the period before.',
				       ` + involvedNone + `, ` + involvedNone + `, now()
				FROM (
				    SELECT ad.warehouse_id,
				           SUM(-l.qty_delta) FILTER (WHERE ad.adjustment_posted_at >= $3::date - $4::int) AS recent,
				           SUM(-l.qty_delta) FILTER (WHERE ad.adjustment_posted_at < $3::date - $4::int) AS prior
				    FROM inventory_adjustment ad
				    JOIN lkp_record_status st ON st.record_status_id = ad.adjustment_status AND st.record_status_code = 'POST'
				    JOIN inventory_adjustment_line l ON l.inventory_adjustment_id = ad.inventory_adjustment_id
				         AND l.line_deleted_at IS NULL AND l.qty_delta < 0
				    WHERE ad.adjustment_deleted_at IS NULL AND ad.adjustment_posted_at >= $3::date - 2 * $4::int
				    GROUP BY ad.warehouse_id) g
				JOIN company_location loc ON loc.company_location_id = g.warehouse_id
				WHERE g.recent >= $5::numeric AND g.recent >= $6::numeric * GREATEST(COALESCE(g.prior, 0), 1)`,
		},
	}
}
