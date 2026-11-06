// Package reports holds the read-only aggregate queries behind the Reports
// module (Reports > Sales, ...). Every report here answers one question and
// returns already-summed rows: list pages are keyset-paginated, so the browser
// can never total them correctly on its own.
package reports

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// Basis picks which document a Sales report counts revenue from.
type Basis string

const (
	// BasisOrders counts booked sales orders (OPEN / PART / FILL): what was sold.
	BasisOrders Basis = "orders"
	// BasisInvoices counts billed invoices (SENT / PART / ODUE / PAID): what was
	// invoiced, matching the dashboard's revenue and Finance.
	BasisInvoices Basis = "invoices"
)

// Bucket is the time granularity of the Sales Trend report.
type Bucket string

const (
	BucketWeek  Bucket = "week"
	BucketMonth Bucket = "month"
)

// maxReportRows caps a report's row count so one broad range can't return an
// unbounded payload; the totals are still computed across every row.
const maxReportRows = 500

// maxDrillRows caps the records listed in a drill-down panel.
const maxDrillRows = 200

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s is a canonical UUID string.
func IsUUID(s string) bool { return uuidPattern.MatchString(s) }

// ParseBasis validates a basis query value; empty defaults to invoices.
func ParseBasis(raw string) (Basis, bool) {
	switch Basis(raw) {
	case "", BasisInvoices:
		return BasisInvoices, true
	case BasisOrders:
		return BasisOrders, true
	}
	return "", false
}

// ParseBucket validates a trend bucket query value; empty defaults to month.
func ParseBucket(raw string) (Bucket, bool) {
	switch Bucket(raw) {
	case "", BucketMonth:
		return BucketMonth, true
	case BucketWeek:
		return BucketWeek, true
	}
	return "", false
}

// Filter is the window and scope every Sales report shares. From is inclusive,
// To is inclusive (the query adds a day); either may be zero for unbounded.
type Filter struct {
	Basis           Basis
	From, To        time.Time
	Scope           authz.Scope
	ActorIdentityID string
}

// basisSpec names the tables/columns of one basis so a single query builder
// serves both documents.
type basisSpec struct {
	header, headerAlias string
	line, lineAlias     string
	headerID, lineFK    string
	numberCol, dateCol  string
	statusCol, custCol  string
	ownerCol, deletedAt string
	lineDeletedAt       string
	statusCodes         []string
	recordPath          string
}

var (
	ordersSpec = basisSpec{
		header: "sales_order", headerAlias: "h", line: "sales_order_item", lineAlias: "l",
		headerID: "sales_order_id", lineFK: "sales_order_id",
		numberCol: "sales_order_number", dateCol: "sales_order_date",
		statusCol: "sales_order_status", custCol: "sales_order_customer_id",
		ownerCol: "sales_order_owner_id", deletedAt: "sales_order_deleted_at",
		lineDeletedAt: "item_deleted_at",
		statusCodes:   []string{"OPEN", "PART", "FILL"},
		recordPath:    "/sales/sales_order",
	}
	invoicesSpec = basisSpec{
		header: "invoice", headerAlias: "h", line: "invoice_item", lineAlias: "l",
		headerID: "invoice_id", lineFK: "invoice_id",
		numberCol: "invoice_number", dateCol: "invoice_date",
		statusCol: "invoice_status", custCol: "invoice_customer_id",
		ownerCol: "invoice_owner_id", deletedAt: "invoice_deleted_at",
		lineDeletedAt: "item_deleted_at",
		statusCodes:   []string{"SENT", "PART", "ODUE", "PAID"},
		recordPath:    "/sales/invoice",
	}
)

func specFor(b Basis) basisSpec {
	if b == BasisOrders {
		return ordersSpec
	}
	return invoicesSpec
}

// RecordPath is the frontend list route for a basis, used by drill-down links.
func RecordPath(b Basis) string { return specFor(b).recordPath }

// Net revenue is the line subtotal minus its discount: tax and shipping are
// pass-through, not sales. Cost is quantity x the item's CURRENT purchasing
// price (free-text lines with no item cost 0), so a past sale's margin moves
// if that price is later edited.
const (
	revenueExpr = "(l.line_subtotal - l.line_discount)"
	costExpr    = "(l.quantity * COALESCE(ii.inventory_item_purchase_unit_price, 0))"
)

// salesFrom builds the shared FROM + WHERE for a basis. The caller appends
// GROUP BY / ORDER BY. ok=false means the caller's scope can't match any row
// (non-"all" scope with no employee record), mirroring the dashboard queries.
func salesFrom(ctx context.Context, pool *pgxpool.Pool, f Filter) (from string, args []any, ok bool, err error) {
	s := specFor(f.Basis)
	args = []any{s.statusCodes}
	where := []string{
		fmt.Sprintf("h.%s IS NULL", s.deletedAt),
		fmt.Sprintf("l.%s IS NULL", s.lineDeletedAt),
		"rs.record_status_code = ANY($1)",
	}
	if f.Scope != authz.ScopeAll {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, f.ActorIdentityID)
		if !found {
			return "", nil, false, nil
		}
		args = append(args, empID)
		where = append(where, fmt.Sprintf("h.%s = $%d", s.ownerCol, len(args)))
	}
	if !f.From.IsZero() {
		args = append(args, f.From)
		where = append(where, fmt.Sprintf("h.%s >= $%d", s.dateCol, len(args)))
	}
	if !f.To.IsZero() {
		args = append(args, f.To.AddDate(0, 0, 1))
		where = append(where, fmt.Sprintf("h.%s < $%d", s.dateCol, len(args)))
	}
	from = fmt.Sprintf(`
		FROM %[1]s h
		JOIN lkp_record_status rs ON rs.record_status_id = h.%[2]s
		JOIN %[3]s l ON l.%[4]s = h.%[5]s
		JOIN customer c ON c.customer_id = h.%[6]s
		LEFT JOIN inventory_item ii ON ii.inventory_item_id = l.inventory_item_id
		WHERE %[7]s`,
		s.header, s.statusCol, s.line, s.lineFK, s.headerID, s.custCol,
		strings.Join(where, " AND "))
	return from, args, true, nil
}

// Totals is the KPI strip every Sales report shows.
type Totals struct {
	Revenue  float64 `json:"revenue"`
	Cost     float64 `json:"cost"`
	Margin   float64 `json:"margin"`
	MarginPc float64 `json:"marginPercent"`
	Records  int     `json:"records"`
}

func newTotals(revenue, cost float64, records int) Totals {
	t := Totals{Revenue: revenue, Cost: cost, Margin: revenue - cost, Records: records}
	if revenue > 0 {
		t.MarginPc = (revenue - cost) / revenue * 100
	}
	return t
}

// CustomerRow is one customer's row in Sales by Customer.
type CustomerRow struct {
	CustomerUUID string     `json:"customerId"`
	Name         string     `json:"name"`
	Orders       int        `json:"records"`
	Revenue      float64    `json:"revenue"`
	Cost         float64    `json:"cost"`
	Margin       float64    `json:"margin"`
	AvgValue     float64    `json:"avgValue"`
	LastDate     *time.Time `json:"lastDate"`
}

// SalesByCustomer ranks customers by net revenue in the window.
func SalesByCustomer(ctx context.Context, pool *pgxpool.Pool, f Filter) ([]CustomerRow, Totals, error) {
	rows := []CustomerRow{}
	from, args, ok, err := salesFrom(ctx, pool, f)
	if err != nil || !ok {
		return rows, Totals{}, err
	}
	s := specFor(f.Basis)
	q := fmt.Sprintf(`
		SELECT c.customer_uuid, c.customer_name, COUNT(DISTINCT h.%[1]s),
		       COALESCE(SUM(%[2]s), 0), COALESCE(SUM(%[3]s), 0), MAX(h.%[4]s)
		%[5]s
		GROUP BY c.customer_uuid, c.customer_name
		ORDER BY SUM(%[2]s) DESC, c.customer_name
		LIMIT %[6]d`, s.headerID, revenueExpr, costExpr, s.dateCol, from, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, Totals{}, fmt.Errorf("query sales by customer: %w", err)
	}
	defer r.Close()
	var revenue, cost float64
	var records int
	for r.Next() {
		var row CustomerRow
		if err := r.Scan(&row.CustomerUUID, &row.Name, &row.Orders, &row.Revenue, &row.Cost, &row.LastDate); err != nil {
			return nil, Totals{}, fmt.Errorf("scan sales by customer: %w", err)
		}
		row.Margin = row.Revenue - row.Cost
		if row.Orders > 0 {
			row.AvgValue = row.Revenue / float64(row.Orders)
		}
		revenue += row.Revenue
		cost += row.Cost
		records += row.Orders
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, Totals{}, fmt.Errorf("query sales by customer: %w", err)
	}
	return rows, newTotals(revenue, cost, records), nil
}

// ItemRow is one inventory item's row in Sales by Item.
type ItemRow struct {
	ItemUUID string  `json:"itemId"`
	SKU      string  `json:"sku"`
	Name     string  `json:"name"`
	Quantity float64 `json:"quantity"`
	Unit     string  `json:"unit"`
	Records  int     `json:"records"`
	Revenue  float64 `json:"revenue"`
	Cost     float64 `json:"cost"`
	Margin   float64 `json:"margin"`
	AvgPrice float64 `json:"avgPrice"`
}

// SalesByItem ranks inventory items by net revenue. Free-text lines (no
// inventory item) are not attributable to an item and are left out of the
// rows or the totals, so these totals can be lower than the same window's
// Sales by Customer / Sales Trend totals; the page says so.
func SalesByItem(ctx context.Context, pool *pgxpool.Pool, f Filter) ([]ItemRow, Totals, error) {
	rows := []ItemRow{}
	from, args, ok, err := salesFrom(ctx, pool, f)
	if err != nil || !ok {
		return rows, Totals{}, err
	}
	s := specFor(f.Basis)
	q := fmt.Sprintf(`
		SELECT ii.inventory_item_uuid, ii.inventory_item_sku, ii.inventory_item_name,
		       COALESCE(SUM(l.quantity), 0), COALESCE(MAX(l.unit_code), ''),
		       COUNT(DISTINCT h.%[1]s),
		       COALESCE(SUM(%[2]s), 0), COALESCE(SUM(%[3]s), 0)
		%[4]s AND l.inventory_item_id IS NOT NULL
		GROUP BY ii.inventory_item_uuid, ii.inventory_item_sku, ii.inventory_item_name
		ORDER BY SUM(%[2]s) DESC, ii.inventory_item_name
		LIMIT %[5]d`, s.headerID, revenueExpr, costExpr, from, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, Totals{}, fmt.Errorf("query sales by item: %w", err)
	}
	defer r.Close()
	var revenue, cost float64
	var records int
	for r.Next() {
		var row ItemRow
		if err := r.Scan(&row.ItemUUID, &row.SKU, &row.Name, &row.Quantity, &row.Unit,
			&row.Records, &row.Revenue, &row.Cost); err != nil {
			return nil, Totals{}, fmt.Errorf("scan sales by item: %w", err)
		}
		row.Margin = row.Revenue - row.Cost
		if row.Quantity > 0 {
			row.AvgPrice = row.Revenue / row.Quantity
		}
		revenue += row.Revenue
		cost += row.Cost
		records += row.Records
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, Totals{}, fmt.Errorf("query sales by item: %w", err)
	}
	return rows, newTotals(revenue, cost, records), nil
}

// TrendRow is one period's point in the Sales Trend chart.
type TrendRow struct {
	Period  string  `json:"period"` // YYYY-MM-DD, first day of the bucket
	Records int     `json:"records"`
	Revenue float64 `json:"revenue"`
	Cost    float64 `json:"cost"`
	Margin  float64 `json:"margin"`
}

// SalesTrend sums revenue and margin per week or month, oldest first.
func SalesTrend(ctx context.Context, pool *pgxpool.Pool, f Filter, bucket Bucket) ([]TrendRow, Totals, error) {
	rows := []TrendRow{}
	from, args, ok, err := salesFrom(ctx, pool, f)
	if err != nil || !ok {
		return rows, Totals{}, err
	}
	s := specFor(f.Basis)
	trunc := "month"
	if bucket == BucketWeek {
		trunc = "week"
	}
	q := fmt.Sprintf(`
		SELECT to_char(date_trunc('%[1]s', h.%[2]s), 'YYYY-MM-DD'), COUNT(DISTINCT h.%[3]s),
		       COALESCE(SUM(%[4]s), 0), COALESCE(SUM(%[5]s), 0)
		%[6]s
		GROUP BY 1
		ORDER BY 1`, trunc, s.dateCol, s.headerID, revenueExpr, costExpr, from)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, Totals{}, fmt.Errorf("query sales trend: %w", err)
	}
	defer r.Close()
	var revenue, cost float64
	var records int
	for r.Next() {
		var row TrendRow
		if err := r.Scan(&row.Period, &row.Records, &row.Revenue, &row.Cost); err != nil {
			return nil, Totals{}, fmt.Errorf("scan sales trend: %w", err)
		}
		row.Margin = row.Revenue - row.Cost
		revenue += row.Revenue
		cost += row.Cost
		records += row.Records
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, Totals{}, fmt.Errorf("query sales trend: %w", err)
	}
	return rows, newTotals(revenue, cost, records), nil
}

// DrillRecord is one document behind a report row, for the side panel.
type DrillRecord struct {
	ID       string    `json:"id"`
	Number   string    `json:"number"`
	Date     time.Time `json:"date"`
	Customer string    `json:"customer"`
	Status   string    `json:"status"`
	Revenue  float64   `json:"revenue"`
}

// DrillSelector narrows the drill-down to one report row: exactly one of
// CustomerUUID, ItemUUID or PeriodStart (with a bucket) is set.
type DrillSelector struct {
	CustomerUUID string
	ItemUUID     string
	PeriodStart  time.Time
	Bucket       Bucket
}

// DrillRecords lists the documents (and each one's net revenue from the
// matching lines) behind one report row.
func DrillRecords(ctx context.Context, pool *pgxpool.Pool, f Filter, sel DrillSelector) ([]DrillRecord, error) {
	out := []DrillRecord{}
	from, args, ok, err := salesFrom(ctx, pool, f)
	if err != nil || !ok {
		return out, err
	}
	s := specFor(f.Basis)
	extra := ""
	switch {
	case sel.CustomerUUID != "":
		args = append(args, sel.CustomerUUID)
		extra = fmt.Sprintf(" AND c.customer_uuid = $%d", len(args))
	case sel.ItemUUID != "":
		args = append(args, sel.ItemUUID)
		extra = fmt.Sprintf(" AND ii.inventory_item_uuid = $%d", len(args))
	case !sel.PeriodStart.IsZero():
		unit := "1 month"
		if sel.Bucket == BucketWeek {
			unit = "7 days"
		}
		args = append(args, sel.PeriodStart)
		extra = fmt.Sprintf(" AND h.%[1]s >= $%[2]d AND h.%[1]s < $%[2]d::date + interval '%[3]s'", s.dateCol, len(args), unit)
	default:
		return out, fmt.Errorf("drill-down needs a customer, item or period")
	}
	uuidCol := s.header + "_uuid"
	q := fmt.Sprintf(`
		SELECT h.%[1]s::text, COALESCE(h.%[2]s, ''), h.%[3]s, c.customer_name, rs.record_status_name,
		       COALESCE(SUM(%[4]s), 0)
		%[5]s%[6]s
		GROUP BY h.%[1]s, h.%[2]s, h.%[3]s, c.customer_name, rs.record_status_name
		ORDER BY h.%[3]s DESC, h.%[2]s DESC
		LIMIT %[7]d`, uuidCol, s.numberCol, s.dateCol, revenueExpr, from, extra, maxDrillRows)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query drill-down: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var d DrillRecord
		if err := r.Scan(&d.ID, &d.Number, &d.Date, &d.Customer, &d.Status, &d.Revenue); err != nil {
			return nil, fmt.Errorf("scan drill-down: %w", err)
		}
		out = append(out, d)
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("query drill-down: %w", err)
	}
	return out, nil
}
