package salesorder

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
)

// nonTerminalStatusCodes are every live sales order status shown in the
// Sales orders snapshot dashboard widget's status breakdown -- every status
// an order can sit in before it reaches Filled or Cancelled (see
// transitions.go's allowedTransitions; FILL and CANC map to an empty set).
var nonTerminalStatusCodes = []string{"DRFT", "PAPV", "APPV", "OPEN", "PART"}

// OpenStatusCodes are the sales order statuses counted as committed backlog
// by the Sales orders snapshot widget -- Draft and Pending approval are
// excluded (not yet a customer commitment), and so are Filled and Cancelled
// (no longer open at all). Exported so controllers/dashboard_salesorders.go
// can filter the same set out of the status breakdown when summing
// open/late totals, without duplicating the literal code list.
var OpenStatusCodes = []string{"APPV", "OPEN", "PART"}

// StatusBucket is one non-terminal status's live aggregate for the widget's
// status breakdown line. LateCount/LateValue are computed for every status
// row uniformly (orders whose expected delivery has passed); it is up to
// the caller to decide which statuses' late figures are meaningful to sum
// (see controllers/dashboard_salesorders.go's summarizeOpenBacklog, which
// only sums across OpenStatusCodes).
type StatusBucket struct {
	Code      string
	Label     string
	Count     int
	Value     float64
	LateCount int
	LateValue float64
}

// AtRiskOrder is one row in the widget's at-risk worklist -- an open
// (OpenStatusCodes) order ranked most-overdue-or-soonest-due first, ties
// broken by value descending (see atRiskOrders).
type AtRiskOrder struct {
	ID           string
	RecordNumber string
	Customer     string
	Value        float64
	Status       string
	DaysLate     *int // positive = days overdue, negative = days until due, nil = no expected delivery date set
}

// SnapshotResult is DashboardSnapshot's raw query result. Summarization
// (open/late totals) is pure Go in controllers/dashboard_salesorders.go, not
// here, so that math stays unit-testable without a database.
type SnapshotResult struct {
	Statuses []StatusBucket
	AtRisk   []AtRiskOrder
}

// DashboardSnapshot loads the Sales orders snapshot dashboard widget's raw
// data: a live status breakdown (every non-terminal status, Draft through
// Partial) and a worklist of the most at-risk open orders, both scoped to
// the caller's RBAC scope and optionally narrowed to orders placed on or
// after since (zero time.Time = unbounded, matching parseDashboardRange).
// Returns a zero SnapshotResult (not an error) when scope is not "all" and
// the caller has no employee record -- mirrors Search's own convention.
func DashboardSnapshot(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, since time.Time, atRiskLimit int) (SnapshotResult, error) {
	var ownerID *int
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return SnapshotResult{}, nil
		}
		ownerID = &empID
	}

	buckets, err := statusBuckets(ctx, pool, nonTerminalStatusCodes, ownerID, since)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("sales order status buckets: %w", err)
	}
	atRisk, err := atRiskOrders(ctx, pool, OpenStatusCodes, ownerID, since, atRiskLimit)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("sales order at-risk list: %w", err)
	}
	return SnapshotResult{Statuses: buckets, AtRisk: atRisk}, nil
}

// scopeRangeWhere builds the owner-scope and order-date-range fragments
// shared by statusBuckets and atRiskOrders, starting parameter numbering at
// startIdx (both queries already use $1 for their own statusCodes filter).
// The date-range parameter is cast to ::date explicitly rather than relying
// on driver/server type inference, since sales_order_date is a bare date
// column being compared against a Go time.Time.
func scopeRangeWhere(ownerID *int, since time.Time, startIdx int) (clauses []string, args []any, nextIdx int) {
	nextIdx = startIdx
	if ownerID != nil {
		args = append(args, *ownerID)
		clauses = append(clauses, fmt.Sprintf("so.sales_order_owner_id = $%d", nextIdx))
		nextIdx++
	}
	if !since.IsZero() {
		args = append(args, since)
		clauses = append(clauses, fmt.Sprintf("so.sales_order_date >= $%d::date", nextIdx))
		nextIdx++
	}
	return clauses, args, nextIdx
}

// statusBuckets aggregates live (non-deleted) sales orders by status,
// scoped to statusCodes/ownerID/since -- one row per status code that has at
// least one matching order.
func statusBuckets(ctx context.Context, pool *pgxpool.Pool, statusCodes []string, ownerID *int, since time.Time) ([]StatusBucket, error) {
	where := []string{"so.sales_order_deleted_at IS NULL", "rs.record_status_code = ANY($1)"}
	args := []any{statusCodes}
	extra, extraArgs, _ := scopeRangeWhere(ownerID, since, 2)
	where = append(where, extra...)
	args = append(args, extraArgs...)

	q := `
		SELECT rs.record_status_code, rs.record_status_name,
		       COUNT(*),
		       COALESCE(SUM(so.sales_order_grand_total), 0),
		       COUNT(*) FILTER (WHERE so.sales_order_expected_delivery < CURRENT_DATE),
		       COALESCE(SUM(so.sales_order_grand_total) FILTER (WHERE so.sales_order_expected_delivery < CURRENT_DATE), 0)
		FROM sales_order so
		JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		WHERE ` + strings.Join(where, " AND ") + `
		GROUP BY rs.record_status_code, rs.record_status_name`

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query status buckets: %w", err)
	}
	defer rows.Close()

	out := []StatusBucket{}
	for rows.Next() {
		var b StatusBucket
		if err := rows.Scan(&b.Code, &b.Label, &b.Count, &b.Value, &b.LateCount, &b.LateValue); err != nil {
			return nil, fmt.Errorf("scan status bucket: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query status buckets: %w", err)
	}
	return out, nil
}

// atRiskOrders returns the limit orders (among statusCodes/ownerID/since)
// most needing attention: soonest-due (or most overdue) first, orders with
// no expected delivery date last, ties broken by value descending.
func atRiskOrders(ctx context.Context, pool *pgxpool.Pool, statusCodes []string, ownerID *int, since time.Time, limit int) ([]AtRiskOrder, error) {
	where := []string{"so.sales_order_deleted_at IS NULL", "rs.record_status_code = ANY($1)"}
	args := []any{statusCodes}
	extra, extraArgs, _ := scopeRangeWhere(ownerID, since, 2)
	where = append(where, extra...)
	args = append(args, extraArgs...)

	q := `
		SELECT so.sales_order_uuid, COALESCE(so.sales_order_number,''), rs.record_status_name,
		       c.customer_name, so.sales_order_grand_total,
		       (CURRENT_DATE - so.sales_order_expected_delivery)::int
		FROM sales_order so
		JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		JOIN customer c ON c.customer_id = so.sales_order_customer_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY (so.sales_order_expected_delivery IS NULL) ASC,
		         so.sales_order_expected_delivery ASC,
		         so.sales_order_grand_total DESC
		LIMIT ` + strconv.Itoa(limit)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query at-risk orders: %w", err)
	}
	defer rows.Close()

	out := []AtRiskOrder{}
	for rows.Next() {
		var o AtRiskOrder
		if err := rows.Scan(&o.ID, &o.RecordNumber, &o.Status, &o.Customer, &o.Value, &o.DaysLate); err != nil {
			return nil, fmt.Errorf("scan at-risk order: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query at-risk orders: %w", err)
	}
	return out, nil
}
