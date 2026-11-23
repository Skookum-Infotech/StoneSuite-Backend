package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// Purchases reports. Definitions mirror the dashboard so the numbers agree:
//   - a PURCHASE ORDER counts once it has been sent to the vendor (SENT, PART,
//     RCVD, CLSD); an OPEN one is SENT/PART (purchaseorder.IncomingStatusCodes);
//   - a VENDOR BILL counts once approved (APPV, PART, PAID, ODUE); a PAYABLE
//     one is APPV/PART/ODUE (vendorbill.PayableStatuses);
//   - amounts are net of tax and shipping (subtotal minus discount), like Sales.
var (
	poCountedCodes   = []string{"SENT", "PART", "RCVD", "CLSD"}
	poOpenCodes      = []string{"SENT", "PART"}
	billCountedCodes = []string{"APPV", "PART", "PAID", "ODUE"}
	billPayableCodes = []string{"APPV", "PART", "ODUE"}
)

// Aging bucket boundaries, in days past the bill's due date.
const (
	agingBucket1Max = 30
	agingBucket2Max = 60
	agingBucket3Max = 90
)

// AgingBucketLabels name the AP aging columns in order.
var AgingBucketLabels = []string{"Current", "1-30 days", "31-60 days", "61-90 days", "Over 90 days"}

// AgingBucketIndex maps days past due to its bucket: 0 for not yet due.
func AgingBucketIndex(daysPastDue int) int {
	switch {
	case daysPastDue <= 0:
		return 0
	case daysPastDue <= agingBucket1Max:
		return 1
	case daysPastDue <= agingBucket2Max:
		return 2
	case daysPastDue <= agingBucket3Max:
		return 3
	}
	return 4
}

// PurchaseFilter is the date window and scope the windowed Purchases reports
// share (From/To inclusive, zero = unbounded).
type PurchaseFilter struct {
	From, To        time.Time
	Scope           authz.Scope
	ActorIdentityID string
}

// ownerClause returns " AND <col> = $n" for a non-"all" scope, appending the
// caller's employee id to args. ok=false when the caller has no employee
// record (so nothing is visible), mirroring the other reports.
func ownerClause(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, actor, col string, args *[]any) (clause string, ok bool) {
	if scope == authz.ScopeAll {
		return "", true
	}
	empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actor)
	if !found {
		return "", false
	}
	*args = append(*args, empID)
	return fmt.Sprintf(" AND %s = $%d", col, len(*args)), true
}

// dateClause returns the inclusive date-window predicate for a column.
func dateClause(col string, from, to time.Time, args *[]any) string {
	out := ""
	if !from.IsZero() {
		*args = append(*args, from)
		out += fmt.Sprintf(" AND %s >= $%d", col, len(*args))
	}
	if !to.IsZero() {
		*args = append(*args, to.AddDate(0, 0, 1))
		out += fmt.Sprintf(" AND %s < $%d", col, len(*args))
	}
	return out
}

// dateArg formats a calendar date as the YYYY-MM-DD text bound to a `$n::date`
// placeholder. "Today" is passed in this way, never read from CURRENT_DATE:
// CURRENT_DATE is the database session's day (UTC), which is already tomorrow
// for a US tenant in the evening, so day counts and overdue flags would be
// off by one for hours every day. Callers pass the tenant-local date.
func dateArg(t time.Time) string { return t.Format(time.DateOnly) }

const vendorNameExpr = `CASE WHEN v.vendor_type = 'Organization' THEN v.vendor_legal_name
	            ELSE TRIM(v.vendor_given_name || ' ' || v.vendor_family_name) END`

// netOutstandingLine is a PO line's net value still to be received: its net
// amount prorated by the unreceived quantity fraction (clamped at zero for an
// over-delivery).
const netOutstandingLine = `GREATEST(l.quantity - l.qty_received, 0) / NULLIF(l.quantity, 0) * (l.line_subtotal - l.line_discount)`

// VendorRow is one vendor's row in Purchases by Vendor.
type VendorRow struct {
	VendorUUID  string  `json:"vendorId"`
	Name        string  `json:"name"`
	POCount     int     `json:"poCount"`
	POValue     float64 `json:"poValue"`
	AvgPOValue  float64 `json:"avgPoValue"`
	OpenPOValue float64 `json:"openPoValue"`
	BillCount   int     `json:"billCount"`
	Billed      float64 `json:"billed"`
	BalanceOwed float64 `json:"balanceOwed"`
}

// VendorTotals sums the vendor table.
type VendorTotals struct {
	POCount     int     `json:"poCount"`
	POValue     float64 `json:"poValue"`
	OpenPOValue float64 `json:"openPoValue"`
	BillCount   int     `json:"billCount"`
	Billed      float64 `json:"billed"`
	BalanceOwed float64 `json:"balanceOwed"`
}

func finishVendorRow(r VendorRow) VendorRow {
	if r.POCount > 0 {
		r.AvgPOValue = r.POValue / float64(r.POCount)
	}
	return r
}

// PurchasesByVendor totals, per vendor, the purchase orders dated in the
// window (count, value, and the part still to be received) and the vendor
// bills dated in the window (billed, and what is still owed on them). The
// listed rows are capped at maxReportRows, but the totals (including what is
// owed to vendors) are summed in SQL over every vendor, so a long vendor list
// never understates them.
func PurchasesByVendor(ctx context.Context, pool *pgxpool.Pool, f PurchaseFilter) ([]VendorRow, VendorTotals, error) {
	rows := []VendorRow{}
	args := []any{poCountedCodes, poOpenCodes, billCountedCodes, billPayableCodes}
	poOwner, ok := ownerClause(ctx, pool, f.Scope, f.ActorIdentityID, "po.purchase_order_owner_id", &args)
	if !ok {
		return rows, VendorTotals{}, nil
	}
	billOwner, _ := ownerClause(ctx, pool, f.Scope, f.ActorIdentityID, "vb.vendor_bill_owner_id", &args)
	poDates := dateClause("po.purchase_order_date", f.From, f.To, &args)
	billDates := dateClause("vb.vendor_bill_date", f.From, f.To, &args)

	base := fmt.Sprintf(`
		WITH po AS (
			SELECT po.purchase_order_vendor_id AS vendor_id, COUNT(*) AS n,
			       SUM(po.purchase_order_subtotal - po.purchase_order_discount_total) AS val,
			       COALESCE(SUM(o.outstanding) FILTER (WHERE rs.record_status_code = ANY($2)), 0) AS open_val
			FROM purchase_order po
			JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
			LEFT JOIN LATERAL (
				SELECT SUM(%[1]s) AS outstanding
				FROM purchase_order_item l
				WHERE l.purchase_order_id = po.purchase_order_id AND l.item_deleted_at IS NULL
			) o ON TRUE
			WHERE po.purchase_order_deleted_at IS NULL AND rs.record_status_code = ANY($1)%[2]s%[3]s
			GROUP BY po.purchase_order_vendor_id
		), vb AS (
			SELECT vb.vendor_bill_vendor_id AS vendor_id, COUNT(*) AS n,
			       SUM(vb.vendor_bill_subtotal - vb.vendor_bill_discount_total) AS billed,
			       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE rs.record_status_code = ANY($4)), 0) AS owed
			FROM vendor_bill vb
			JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
			WHERE vb.vendor_bill_deleted_at IS NULL AND rs.record_status_code = ANY($3)%[4]s%[5]s
			GROUP BY vb.vendor_bill_vendor_id
		), per_vendor AS (
			SELECT v.vendor_uuid::text AS vendor_id, %[6]s AS name,
			       COALESCE(po.n, 0) AS po_count, COALESCE(po.val, 0) AS po_value, COALESCE(po.open_val, 0) AS open_po_value,
			       COALESCE(vb.n, 0) AS bill_count, COALESCE(vb.billed, 0) AS billed, COALESCE(vb.owed, 0) AS owed
			FROM vendor v
			JOIN (SELECT vendor_id FROM po UNION SELECT vendor_id FROM vb) ids ON ids.vendor_id = v.vendor_id
			LEFT JOIN po ON po.vendor_id = v.vendor_id
			LEFT JOIN vb ON vb.vendor_id = v.vendor_id
		)`, netOutstandingLine, poOwner, poDates, billOwner, billDates, vendorNameExpr)
	listQ := base + fmt.Sprintf(`
		SELECT vendor_id, name, po_count, po_value, open_po_value, bill_count, billed, owed
		FROM per_vendor
		ORDER BY billed DESC, po_value DESC, name
		LIMIT %d`, maxReportRows)
	r, err := pool.Query(ctx, listQ, args...)
	if err != nil {
		return nil, VendorTotals{}, fmt.Errorf("query purchases by vendor: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row VendorRow
		if err := r.Scan(&row.VendorUUID, &row.Name, &row.POCount, &row.POValue, &row.OpenPOValue,
			&row.BillCount, &row.Billed, &row.BalanceOwed); err != nil {
			return nil, VendorTotals{}, fmt.Errorf("scan purchases by vendor: %w", err)
		}
		rows = append(rows, finishVendorRow(row))
	}
	if err := r.Err(); err != nil {
		return nil, VendorTotals{}, fmt.Errorf("query purchases by vendor: %w", err)
	}
	var t VendorTotals
	err = pool.QueryRow(ctx, base+`
		SELECT COALESCE(SUM(po_count), 0)::bigint, COALESCE(SUM(po_value), 0), COALESCE(SUM(open_po_value), 0),
		       COALESCE(SUM(bill_count), 0)::bigint, COALESCE(SUM(billed), 0), COALESCE(SUM(owed), 0)
		FROM per_vendor`, args...).Scan(&t.POCount, &t.POValue, &t.OpenPOValue, &t.BillCount, &t.Billed, &t.BalanceOwed)
	if err != nil {
		return nil, VendorTotals{}, fmt.Errorf("query purchases by vendor totals: %w", err)
	}
	return rows, t, nil
}

// PurchaseItemRow is one inventory item's row in Purchases by Item.
type PurchaseItemRow struct {
	ItemUUID   string   `json:"itemId"`
	SKU        string   `json:"sku"`
	Name       string   `json:"name"`
	Quantity   float64  `json:"quantity"`
	Unit       string   `json:"unit"`
	Received   float64  `json:"received"`
	POCount    int      `json:"poCount"`
	Spend      float64  `json:"spend"`
	AvgCost    float64  `json:"avgCost"`
	MinCost    float64  `json:"minCost"`
	MaxCost    float64  `json:"maxCost"`
	FirstCost  float64  `json:"firstCost"`
	LastCost   float64  `json:"lastCost"`
	CostChange *float64 `json:"costChangePercent"`
}

// PurchaseItemTotals sums the item table.
type PurchaseItemTotals struct {
	Spend   float64 `json:"spend"`
	Items   int     `json:"items"`
	POCount int     `json:"poCount"`
}

// costChangePercent is the move from the first to the last unit cost in the
// window; nil when there is no starting cost to compare against.
func costChangePercent(first, last float64) *float64 {
	if first <= 0 {
		return nil
	}
	v := (last - first) / first * 100
	return &v
}

func finishItemRow(r PurchaseItemRow) PurchaseItemRow {
	if r.Quantity > 0 {
		r.AvgCost = r.Spend / r.Quantity
	}
	r.CostChange = costChangePercent(r.FirstCost, r.LastCost)
	return r
}

// PurchasesByItem ranks inventory items by net spend on purchase orders dated
// in the window. Free-text lines (no inventory item) are not attributable to
// an item and are left out.
func PurchasesByItem(ctx context.Context, pool *pgxpool.Pool, f PurchaseFilter) ([]PurchaseItemRow, PurchaseItemTotals, error) {
	rows := []PurchaseItemRow{}
	args := []any{poCountedCodes}
	owner, ok := ownerClause(ctx, pool, f.Scope, f.ActorIdentityID, "po.purchase_order_owner_id", &args)
	if !ok {
		return rows, PurchaseItemTotals{}, nil
	}
	dates := dateClause("po.purchase_order_date", f.From, f.To, &args)
	q := fmt.Sprintf(`
		SELECT ii.inventory_item_uuid::text, ii.inventory_item_sku, ii.inventory_item_name,
		       COALESCE(SUM(l.quantity), 0), COALESCE(MAX(l.unit_code), ''), COALESCE(SUM(l.qty_received), 0),
		       COUNT(DISTINCT po.purchase_order_id), COALESCE(SUM(l.line_subtotal - l.line_discount), 0),
		       MIN(l.unit_price), MAX(l.unit_price),
		       (ARRAY_AGG(l.unit_price ORDER BY po.purchase_order_date, po.purchase_order_id))[1],
		       (ARRAY_AGG(l.unit_price ORDER BY po.purchase_order_date DESC, po.purchase_order_id DESC))[1]
		FROM purchase_order po
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		JOIN purchase_order_item l ON l.purchase_order_id = po.purchase_order_id AND l.item_deleted_at IS NULL
		JOIN inventory_item ii ON ii.inventory_item_id = l.inventory_item_id
		WHERE po.purchase_order_deleted_at IS NULL AND rs.record_status_code = ANY($1)%s%s
		GROUP BY ii.inventory_item_uuid, ii.inventory_item_sku, ii.inventory_item_name
		ORDER BY SUM(l.line_subtotal - l.line_discount) DESC, ii.inventory_item_name
		LIMIT %d`, owner, dates, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, PurchaseItemTotals{}, fmt.Errorf("query purchases by item: %w", err)
	}
	defer r.Close()
	var t PurchaseItemTotals
	for r.Next() {
		var row PurchaseItemRow
		if err := r.Scan(&row.ItemUUID, &row.SKU, &row.Name, &row.Quantity, &row.Unit, &row.Received,
			&row.POCount, &row.Spend, &row.MinCost, &row.MaxCost, &row.FirstCost, &row.LastCost); err != nil {
			return nil, PurchaseItemTotals{}, fmt.Errorf("scan purchases by item: %w", err)
		}
		t.Spend += row.Spend
		t.POCount += row.POCount
		rows = append(rows, finishItemRow(row))
	}
	if err := r.Err(); err != nil {
		return nil, PurchaseItemTotals{}, fmt.Errorf("query purchases by item: %w", err)
	}
	t.Items = len(rows)
	return rows, t, nil
}

// OpenPORow is one purchase order still waiting on goods.
type OpenPORow struct {
	ID              string     `json:"id"`
	Number          string     `json:"number"`
	Vendor          string     `json:"vendor"`
	Status          string     `json:"status"`
	OrderDate       time.Time  `json:"orderDate"`
	ExpectedDate    *time.Time `json:"expectedDate"`
	Value           float64    `json:"value"`
	Outstanding     float64    `json:"outstanding"`
	ReceivedPercent float64    `json:"receivedPercent"`
	DaysOverdue     int        `json:"daysOverdue"`
}

// OpenPOTotals summarises the open-PO list.
type OpenPOTotals struct {
	Count        int     `json:"count"`
	Outstanding  float64 `json:"outstanding"`
	OverdueCount int     `json:"overdueCount"`
	OverdueValue float64 `json:"overdueValue"`
}

// OpenPurchaseOrders lists SENT/PART purchase orders as of `today` (the
// tenant's local date, not the database's UTC day), most overdue first. The
// received percentage is the average of each line's received share (capped at
// 100%), so one over-delivered line cannot hide a short one. The listed rows
// are capped at maxReportRows; the totals are summed in SQL over every open
// order so they never understate a long list.
func OpenPurchaseOrders(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, actor string, today time.Time) ([]OpenPORow, OpenPOTotals, error) {
	rows := []OpenPORow{}
	args := []any{poOpenCodes}
	owner, ok := ownerClause(ctx, pool, scope, actor, "po.purchase_order_owner_id", &args)
	if !ok {
		return rows, OpenPOTotals{}, nil
	}
	args = append(args, dateArg(today))
	todayRef := fmt.Sprintf("$%d::date", len(args))
	base := fmt.Sprintf(`
		WITH open_po AS (
			SELECT po.purchase_order_uuid::text AS id, COALESCE(po.purchase_order_number, '') AS number,
			       po.purchase_order_vendor_name AS vendor, rs.record_status_name AS status,
			       po.purchase_order_date AS order_date, po.purchase_order_expected_date AS expected_date,
			       COALESCE(SUM(l.line_subtotal - l.line_discount), 0) AS value,
			       COALESCE(SUM(%[1]s), 0) AS outstanding,
			       COALESCE(AVG(LEAST(l.qty_received / NULLIF(l.quantity, 0), 1)) FILTER (WHERE l.quantity > 0), 0) * 100 AS received_percent,
			       CASE WHEN po.purchase_order_expected_date < %[3]s
			            THEN %[3]s - po.purchase_order_expected_date ELSE 0 END AS days_overdue
			FROM purchase_order po
			JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
			LEFT JOIN purchase_order_item l ON l.purchase_order_id = po.purchase_order_id AND l.item_deleted_at IS NULL
			WHERE po.purchase_order_deleted_at IS NULL AND rs.record_status_code = ANY($1)%[2]s
			GROUP BY po.purchase_order_id, rs.record_status_name
		)`, netOutstandingLine, owner, todayRef)
	listQ := base + fmt.Sprintf(`
		SELECT id, number, vendor, status, order_date, expected_date, value, outstanding, received_percent, days_overdue
		FROM open_po
		ORDER BY days_overdue DESC, expected_date NULLS LAST, number
		LIMIT %d`, maxReportRows)
	r, err := pool.Query(ctx, listQ, args...)
	if err != nil {
		return nil, OpenPOTotals{}, fmt.Errorf("query open purchase orders: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row OpenPORow
		if err := r.Scan(&row.ID, &row.Number, &row.Vendor, &row.Status, &row.OrderDate, &row.ExpectedDate,
			&row.Value, &row.Outstanding, &row.ReceivedPercent, &row.DaysOverdue); err != nil {
			return nil, OpenPOTotals{}, fmt.Errorf("scan open purchase order: %w", err)
		}
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, OpenPOTotals{}, fmt.Errorf("query open purchase orders: %w", err)
	}
	var t OpenPOTotals
	err = pool.QueryRow(ctx, base+`
		SELECT COUNT(*)::bigint, COALESCE(SUM(outstanding), 0),
		       (COUNT(*) FILTER (WHERE days_overdue > 0))::bigint,
		       COALESCE(SUM(outstanding) FILTER (WHERE days_overdue > 0), 0)
		FROM open_po`, args...).Scan(&t.Count, &t.Outstanding, &t.OverdueCount, &t.OverdueValue)
	if err != nil {
		return nil, OpenPOTotals{}, fmt.Errorf("query open purchase order totals: %w", err)
	}
	return rows, t, nil
}

// AgingRow is one vendor's unpaid balance split into aging buckets.
type AgingRow struct {
	VendorUUID string    `json:"vendorId"`
	Name       string    `json:"name"`
	Bills      int       `json:"bills"`
	Buckets    []float64 `json:"buckets"` // one per AgingBucketLabels
	Total      float64   `json:"total"`
}

// AgingTotals is the all-vendor bucket sum.
type AgingTotals struct {
	Bills   int       `json:"bills"`
	Buckets []float64 `json:"buckets"`
	Total   float64   `json:"total"`
}

func sumAging(rows []AgingRow) AgingTotals {
	t := AgingTotals{Buckets: make([]float64, len(AgingBucketLabels))}
	for _, r := range rows {
		t.Bills += r.Bills
		t.Total += r.Total
		for i, v := range r.Buckets {
			t.Buckets[i] += v
		}
	}
	return t
}

// APAging splits every payable vendor bill's balance due into buckets by how
// many days past its due date (bill date if it has none) it is as of `today`
// (the tenant's local date, not the database's UTC day). The listed vendors
// are capped at maxReportRows; the totals are summed in SQL over every vendor,
// so a long vendor list never understates what is owed.
func APAging(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, actor string, today time.Time) ([]AgingRow, AgingTotals, error) {
	rows := []AgingRow{}
	args := []any{billPayableCodes}
	owner, ok := ownerClause(ctx, pool, scope, actor, "vb.vendor_bill_owner_id", &args)
	if !ok {
		return rows, sumAging(nil), nil
	}
	args = append(args, dateArg(today))
	past := fmt.Sprintf("($%d::date - COALESCE(vb.vendor_bill_due_date, vb.vendor_bill_date))", len(args))
	base := fmt.Sprintf(`
		WITH per_vendor AS (
			SELECT v.vendor_uuid::text AS vendor_id, %[1]s AS name, COUNT(*) AS bills,
			       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE %[2]s <= 0), 0) AS b0,
			       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE %[2]s BETWEEN 1 AND %[3]d), 0) AS b1,
			       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE %[2]s BETWEEN %[3]d + 1 AND %[4]d), 0) AS b2,
			       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE %[2]s BETWEEN %[4]d + 1 AND %[5]d), 0) AS b3,
			       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE %[2]s > %[5]d), 0) AS b4,
			       SUM(vb.vendor_bill_balance_due) AS total
			FROM vendor_bill vb
			JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
			JOIN vendor v ON v.vendor_id = vb.vendor_bill_vendor_id
			WHERE vb.vendor_bill_deleted_at IS NULL AND rs.record_status_code = ANY($1)
			  AND vb.vendor_bill_balance_due > 0%[6]s
			GROUP BY v.vendor_id, v.vendor_uuid
		)`, vendorNameExpr, past, agingBucket1Max, agingBucket2Max, agingBucket3Max, owner)
	listQ := base + fmt.Sprintf(`
		SELECT vendor_id, name, bills, b0, b1, b2, b3, b4, total
		FROM per_vendor
		ORDER BY total DESC, name
		LIMIT %d`, maxReportRows)
	r, err := pool.Query(ctx, listQ, args...)
	if err != nil {
		return nil, AgingTotals{}, fmt.Errorf("query ap aging: %w", err)
	}
	defer r.Close()
	for r.Next() {
		row := AgingRow{Buckets: make([]float64, len(AgingBucketLabels))}
		if err := r.Scan(&row.VendorUUID, &row.Name, &row.Bills, &row.Buckets[0], &row.Buckets[1], &row.Buckets[2],
			&row.Buckets[3], &row.Buckets[4], &row.Total); err != nil {
			return nil, AgingTotals{}, fmt.Errorf("scan ap aging: %w", err)
		}
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, AgingTotals{}, fmt.Errorf("query ap aging: %w", err)
	}
	t := AgingTotals{Buckets: make([]float64, len(AgingBucketLabels))}
	err = pool.QueryRow(ctx, base+`
		SELECT COALESCE(SUM(bills), 0)::bigint, COALESCE(SUM(b0), 0), COALESCE(SUM(b1), 0), COALESCE(SUM(b2), 0),
		       COALESCE(SUM(b3), 0), COALESCE(SUM(b4), 0), COALESCE(SUM(total), 0)
		FROM per_vendor`, args...).Scan(&t.Bills, &t.Buckets[0], &t.Buckets[1], &t.Buckets[2], &t.Buckets[3], &t.Buckets[4], &t.Total)
	if err != nil {
		return nil, AgingTotals{}, fmt.Errorf("query ap aging totals: %w", err)
	}
	return rows, t, nil
}
