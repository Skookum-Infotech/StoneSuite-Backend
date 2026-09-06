package invoice

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// TopCustomer is one customer's billed-revenue aggregate for the Top
// customers dashboard widget (see billedStatusCodes in revenue.go for what
// "billed" means).
type TopCustomer struct {
	CustomerUUID string
	Name         string
	Value        float64
}

// TopCustomersResult is TopCustomersByRevenue's full result: the top `limit`
// customers by billed revenue, plus TotalValue/CustomerCount computed across
// EVERY customer with billed revenue in the window (not just the returned
// top N) -- used for the widget's "Top N · X% of revenue" concentration
// line, which needs a denominator wider than what's actually shown.
type TopCustomersResult struct {
	Customers     []TopCustomer
	TotalValue    float64
	CustomerCount int
}

// TopCustomersByRevenue ranks customers by billed invoice revenue in
// [since, now), scoped to the caller's RBAC scope. A zero since is
// unbounded (matches parseDashboardRange's "all" convention). Returns a
// zero TopCustomersResult (not an error) when scope is not "all" and the
// caller has no employee record, mirroring RevenueBetween/Search's own
// convention.
func TopCustomersByRevenue(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, since time.Time, limit int) (TopCustomersResult, error) {
	where := []string{"i.invoice_deleted_at IS NULL", "rs.record_status_code = ANY($1)"}
	args := []any{billedStatusCodes}
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return TopCustomersResult{}, nil
		}
		args = append(args, empID)
		where = append(where, fmt.Sprintf("i.invoice_owner_id = $%d", len(args)))
	}
	if !since.IsZero() {
		args = append(args, since)
		where = append(where, fmt.Sprintf("i.invoice_created_at >= $%d", len(args)))
	}

	// SUM(SUM(...)) OVER () / COUNT(*) OVER () run after GROUP BY collapses
	// rows to one per customer but before ORDER BY/LIMIT truncate the output
	// (Postgres's SELECT execution order), so every returned row -- even
	// though only the top `limit` rows are returned -- carries the true
	// total across every customer's group, not just the ones shown. One
	// round trip instead of a separate COUNT/SUM query.
	q := `
		SELECT c.customer_uuid, c.customer_name, SUM(i.invoice_grand_total),
		       SUM(SUM(i.invoice_grand_total)) OVER (), COUNT(*) OVER ()
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		JOIN customer c ON c.customer_id = i.invoice_customer_id
		WHERE ` + strings.Join(where, " AND ") + `
		GROUP BY c.customer_uuid, c.customer_name
		ORDER BY SUM(i.invoice_grand_total) DESC
		LIMIT ` + strconv.Itoa(limit)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return TopCustomersResult{}, fmt.Errorf("query top customers: %w", err)
	}
	defer rows.Close()

	result := TopCustomersResult{Customers: []TopCustomer{}}
	for rows.Next() {
		var c TopCustomer
		if err := rows.Scan(&c.CustomerUUID, &c.Name, &c.Value, &result.TotalValue, &result.CustomerCount); err != nil {
			return TopCustomersResult{}, fmt.Errorf("scan top customer: %w", err)
		}
		result.Customers = append(result.Customers, c)
	}
	if err := rows.Err(); err != nil {
		return TopCustomersResult{}, fmt.Errorf("query top customers: %w", err)
	}
	return result, nil
}

// PriorRevenueByCustomer returns billed invoice revenue per customer (keyed
// by customer_uuid) within [since, until), scoped to the caller's RBAC
// scope, for exactly the given customer UUIDs. A customer_uuid absent from
// the returned map billed nothing in that window -- the caller (see
// controllers/dashboard_topcustomers.go's mapTopCustomers) reads that as a
// real zero, not a missing-data placeholder.
func PriorRevenueByCustomer(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, customerUUIDs []string, since, until time.Time) (map[string]float64, error) {
	out := map[string]float64{}
	if len(customerUUIDs) == 0 {
		return out, nil
	}

	where := []string{
		"i.invoice_deleted_at IS NULL",
		"rs.record_status_code = ANY($1)",
		"c.customer_uuid = ANY($2)",
		"i.invoice_created_at >= $3",
		"i.invoice_created_at < $4",
	}
	args := []any{billedStatusCodes, customerUUIDs, since, until}
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return out, nil
		}
		args = append(args, empID)
		where = append(where, fmt.Sprintf("i.invoice_owner_id = $%d", len(args)))
	}

	q := `
		SELECT c.customer_uuid, SUM(i.invoice_grand_total)
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		JOIN customer c ON c.customer_id = i.invoice_customer_id
		WHERE ` + strings.Join(where, " AND ") + `
		GROUP BY c.customer_uuid`

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query prior customer revenue: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var uuid string
		var value float64
		if err := rows.Scan(&uuid, &value); err != nil {
			return nil, fmt.Errorf("scan prior customer revenue: %w", err)
		}
		out[uuid] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query prior customer revenue: %w", err)
	}
	return out, nil
}
