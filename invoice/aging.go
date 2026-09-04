package invoice

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// outstandingStatusCodes are the invoice statuses that still owe money:
// billed but not yet settled. PAID is excluded (nothing left to collect), and
// so are DRFT/PAPV/APPV (never sent, so never a receivable) and VOID -- cf.
// billedStatusCodes in revenue.go, which is the wider "was ever billed" set.
var outstandingStatusCodes = []string{"SENT", "PART", "ODUE"}

// AgingBucketLabels are the four A/R aging buckets, youngest first. The
// dashboard widget renders one bar per label in exactly this order, so
// OutstandingAging always returns all four -- a bucket with nothing in it
// comes back as a real zero rather than being omitted.
var AgingBucketLabels = []string{"0-30", "31-60", "61-90", "90+"}

// AgingBucket is one aging band's outstanding balance and invoice count.
type AgingBucket struct {
	Label  string
	Amount float64
	Count  int
}

// AgingResult is OutstandingAging's full result. Outstanding/OutstandingCount
// cover every outstanding invoice; OverdueTotal/OverdueCount cover only the
// subset actually past its due date, which is what the widget's Overdue tile
// reads.
type AgingResult struct {
	Buckets          []AgingBucket
	Outstanding      float64
	OutstandingCount int
	OverdueTotal     float64
	OverdueCount     int
}

// OutstandingInvoice is one row of the widget's oldest-outstanding worklist.
type OutstandingInvoice struct {
	UUID        string
	Number      string
	Customer    string
	BalanceDue  float64
	DaysPastDue int
}

// outstandingWhere builds the shared WHERE clauses for both aging queries:
// live, still-owing invoices, narrowed to the caller's RBAC scope. Returns
// ok=false when scope is not "all" and the caller has no employee record --
// callers return a zero result rather than an error, mirroring
// TopCustomersByRevenue/RevenueBetween's convention.
func outstandingWhere(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string) (where []string, args []any, ok bool) {
	where = []string{
		"i.invoice_deleted_at IS NULL",
		"rs.record_status_code = ANY($1)",
		// A fully-settled invoice that hasn't been transitioned to PAID yet
		// still carries an outstanding status; it owes nothing, so it must
		// not inflate the counts.
		"i.invoice_balance_due > 0",
	}
	args = []any{outstandingStatusCodes}
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return nil, nil, false
		}
		args = append(args, empID)
		where = append(where, fmt.Sprintf("i.invoice_owner_id = $%d", len(args)))
	}
	return where, args, true
}

// OutstandingAging buckets every outstanding invoice's balance by how far
// past its due date it is, scoped to the caller's RBAC scope.
//
// An invoice with no due date set is neither overdue nor aged: it lands in
// the youngest bucket and is excluded from OverdueTotal/OverdueCount. Terms
// were never agreed on it, so calling it 90+ days late would be an
// accusation the data doesn't support.
func OutstandingAging(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string) (AgingResult, error) {
	result := AgingResult{Buckets: emptyBuckets()}

	where, args, ok := outstandingWhere(ctx, pool, scope, actorIdentityID)
	if !ok {
		return result, nil
	}

	// The window functions run after GROUP BY collapses rows to one per
	// bucket, so every returned row carries the same tenant-wide totals --
	// one round trip instead of a separate totals query (the same trick
	// TopCustomersByRevenue uses). FILTER narrows the overdue aggregates to
	// invoices genuinely past due; COALESCE covers the all-NULL case where
	// no bucket has an overdue invoice in it.
	q := `
		WITH outstanding AS (
			SELECT i.invoice_balance_due AS balance,
			       (i.invoice_due_date IS NOT NULL AND i.invoice_due_date < CURRENT_DATE) AS is_overdue,
			       CASE
			           WHEN i.invoice_due_date IS NULL THEN '0-30'
			           WHEN CURRENT_DATE - i.invoice_due_date <= 30 THEN '0-30'
			           WHEN CURRENT_DATE - i.invoice_due_date <= 60 THEN '31-60'
			           WHEN CURRENT_DATE - i.invoice_due_date <= 90 THEN '61-90'
			           ELSE '90+'
			       END AS bucket
			FROM invoice i
			JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
			WHERE ` + strings.Join(where, " AND ") + `
		)
		SELECT bucket, SUM(balance), COUNT(*),
		       SUM(SUM(balance)) OVER (),
		       COUNT(*) OVER (),
		       COALESCE(SUM(SUM(balance) FILTER (WHERE is_overdue)) OVER (), 0),
		       COALESCE(SUM(COUNT(*) FILTER (WHERE is_overdue)) OVER (), 0)
		FROM outstanding
		GROUP BY bucket`

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return AgingResult{}, fmt.Errorf("query invoice aging: %w", err)
	}
	defer rows.Close()

	byLabel := map[string]AgingBucket{}
	for rows.Next() {
		var b AgingBucket
		// bucketRows is COUNT(*) OVER () -- how many buckets came back, not
		// how many invoices. The invoice count is summed from the buckets
		// below, so this is scanned only to satisfy the column list.
		var bucketRows int
		if err := rows.Scan(&b.Label, &b.Amount, &b.Count,
			&result.Outstanding, &bucketRows, &result.OverdueTotal, &result.OverdueCount); err != nil {
			return AgingResult{}, fmt.Errorf("scan invoice aging: %w", err)
		}
		byLabel[b.Label] = b
	}
	if err := rows.Err(); err != nil {
		return AgingResult{}, fmt.Errorf("query invoice aging: %w", err)
	}

	for i, label := range AgingBucketLabels {
		if b, found := byLabel[label]; found {
			result.Buckets[i] = b
			result.OutstandingCount += b.Count
		}
	}
	return result, nil
}

// emptyBuckets is the all-zero four-bucket skeleton every AgingResult starts
// from, so a tenant with no outstanding invoices still renders four bars.
func emptyBuckets() []AgingBucket {
	buckets := make([]AgingBucket, len(AgingBucketLabels))
	for i, label := range AgingBucketLabels {
		buckets[i] = AgingBucket{Label: label}
	}
	return buckets
}

// OldestOutstanding returns the `limit` outstanding invoices furthest past
// their due date, scoped to the caller's RBAC scope -- the widget's worklist,
// each row linking to its invoice. An invoice with no due date reads as 0
// days past due (see OutstandingAging), so it sorts below every genuinely
// late one and only surfaces when nothing is actually overdue.
func OldestOutstanding(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, limit int) ([]OutstandingInvoice, error) {
	where, args, ok := outstandingWhere(ctx, pool, scope, actorIdentityID)
	if !ok {
		return []OutstandingInvoice{}, nil
	}

	q := `
		SELECT i.invoice_uuid, COALESCE(i.invoice_number,''), c.customer_name,
		       i.invoice_balance_due,
		       COALESCE(CURRENT_DATE - i.invoice_due_date, 0) AS days_past_due
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		JOIN customer c ON c.customer_id = i.invoice_customer_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY days_past_due DESC, i.invoice_balance_due DESC
		LIMIT ` + strconv.Itoa(limit)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query oldest outstanding invoices: %w", err)
	}
	defer rows.Close()

	out := []OutstandingInvoice{}
	for rows.Next() {
		var inv OutstandingInvoice
		if err := rows.Scan(&inv.UUID, &inv.Number, &inv.Customer, &inv.BalanceDue, &inv.DaysPastDue); err != nil {
			return nil, fmt.Errorf("scan oldest outstanding invoice: %w", err)
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query oldest outstanding invoices: %w", err)
	}
	return out, nil
}
