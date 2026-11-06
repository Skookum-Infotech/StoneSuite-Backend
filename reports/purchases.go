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

func sumVendors(rows []VendorRow) VendorTotals {
	var t VendorTotals
	for _, r := range rows {
		t.POCount += r.POCount
		t.POValue += r.POValue
		t.OpenPOValue += r.OpenPOValue
		t.BillCount += r.BillCount
		t.Billed += r.Billed
		t.BalanceOwed += r.BalanceOwed
	}
	return t
}

// PurchasesByVendor totals, per vendor, the purchase orders dated in the
// window (count, value, and the part still to be received) and the vendor
// bills dated in the window (billed, and what is still owed on them).
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

	q := fmt.Sprintf(`
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
		)
		SELECT v.vendor_uuid::text, %[6]s,
		       COALESCE(po.n, 0), COALESCE(po.val, 0), COALESCE(po.open_val, 0),
		       COALESCE(vb.n, 0), COALESCE(vb.billed, 0), COALESCE(vb.owed, 0)
		FROM vendor v
		JOIN (SELECT vendor_id FROM po UNION SELECT vendor_id FROM vb) ids ON ids.vendor_id = v.vendor_id
		LEFT JOIN po ON po.vendor_id = v.vendor_id
		LEFT JOIN vb ON vb.vendor_id = v.vendor_id
		ORDER BY COALESCE(vb.billed, 0) DESC, COALESCE(po.val, 0) DESC, 2
		LIMIT %[7]d`, netOutstandingLine, poOwner, poDates, billOwner, billDates, vendorNameExpr, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
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
	return rows, sumVendors(rows), nil
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

func sumOpenPOs(rows []OpenPORow) OpenPOTotals {
	var t OpenPOTotals
	t.Count = len(rows)
	for _, r := range rows {
		t.Outstanding += r.Outstanding
		if r.DaysOverdue > 0 {
			t.OverdueCount++
			t.OverdueValue += r.Outstanding
		}
	}
	return t
}

// OpenPurchaseOrders lists SENT/PART purchase orders as of today, most overdue
// first. The received percentage is the average of each line's received share
// (capped at 100%), so one over-delivered line cannot hide a short one.
func OpenPurchaseOrders(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, actor string) ([]OpenPORow, OpenPOTotals, error) {
	rows := []OpenPORow{}
	args := []any{poOpenCodes}
	owner, ok := ownerClause(ctx, pool, scope, actor, "po.purchase_order_owner_id", &args)
	if !ok {
		return rows, OpenPOTotals{}, nil
	}
	q := fmt.Sprintf(`
		SELECT po.purchase_order_uuid::text, COALESCE(po.purchase_order_number, ''), po.purchase_order_vendor_name,
		       rs.record_status_name, po.purchase_order_date, po.purchase_order_expected_date,
		       COALESCE(SUM(l.line_subtotal - l.line_discount), 0),
		       COALESCE(SUM(%[1]s), 0),
		       COALESCE(AVG(LEAST(l.qty_received / NULLIF(l.quantity, 0), 1)) FILTER (WHERE l.quantity > 0), 0) * 100,
		       CASE WHEN po.purchase_order_expected_date < CURRENT_DATE
		            THEN CURRENT_DATE - po.purchase_order_expected_date ELSE 0 END
		FROM purchase_order po
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		LEFT JOIN purchase_order_item l ON l.purchase_order_id = po.purchase_order_id AND l.item_deleted_at IS NULL
		WHERE po.purchase_order_deleted_at IS NULL AND rs.record_status_code = ANY($1)%[2]s
		GROUP BY po.purchase_order_id, rs.record_status_name
		ORDER BY 10 DESC, po.purchase_order_expected_date NULLS LAST, po.purchase_order_number
		LIMIT %[3]d`, netOutstandingLine, owner, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
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
	return rows, sumOpenPOs(rows), nil
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
// many days past its due date (bill date if it has none) it is as of today.
func APAging(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, actor string) ([]AgingRow, AgingTotals, error) {
	rows := []AgingRow{}
	args := []any{billPayableCodes}
	owner, ok := ownerClause(ctx, pool, scope, actor, "vb.vendor_bill_owner_id", &args)
	if !ok {
		return rows, sumAging(nil), nil
	}
	past := "(CURRENT_DATE - COALESCE(vb.vendor_bill_due_date, vb.vendor_bill_date))"
	q := fmt.Sprintf(`
		SELECT v.vendor_uuid::text, %[1]s, COUNT(*),
		       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE %[2]s <= 0), 0),
		       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE %[2]s BETWEEN 1 AND %[3]d), 0),
		       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE %[2]s BETWEEN %[3]d + 1 AND %[4]d), 0),
		       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE %[2]s BETWEEN %[4]d + 1 AND %[5]d), 0),
		       COALESCE(SUM(vb.vendor_bill_balance_due) FILTER (WHERE %[2]s > %[5]d), 0),
		       SUM(vb.vendor_bill_balance_due)
		FROM vendor_bill vb
		JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		JOIN vendor v ON v.vendor_id = vb.vendor_bill_vendor_id
		WHERE vb.vendor_bill_deleted_at IS NULL AND rs.record_status_code = ANY($1)
		  AND vb.vendor_bill_balance_due > 0%[6]s
		GROUP BY v.vendor_id, v.vendor_uuid
		ORDER BY SUM(vb.vendor_bill_balance_due) DESC, 2
		LIMIT %[7]d`, vendorNameExpr, past, agingBucket1Max, agingBucket2Max, agingBucket3Max, owner, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
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
	return rows, sumAging(rows), nil
}
