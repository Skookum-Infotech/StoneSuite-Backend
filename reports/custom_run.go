package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Result is what a custom report returns. Every cell is a string, a float64 or
// nil — BuildQuery casts in SQL so no driver type ever reaches the client.
type Result struct {
	Columns   []ResultColumn `json:"columns"`
	Rows      [][]any        `json:"rows"`
	Totals    []any          `json:"totals"`   // aligned to Columns; nil where a column has no total
	RowCount  int            `json:"rowCount"` // rows the report matched, before grouping, top-N or the row limit
	Truncated bool           `json:"truncated"`
}

// reportQueryTimeout bounds one custom report (its rows and its totals): users
// build these queries, so a broad ILIKE across joins must not hold a tenant
// connection indefinitely. pgx cancels the running statement when it expires.
const reportQueryTimeout = 30 * time.Second

// Run compiles def and executes it for the caller's scope.
func Run(ctx context.Context, pool *pgxpool.Pool, src Source, def Definition, scope ScopeArgs, limit int) (Result, error) {
	c, err := BuildQuery(src, def, scope, limit)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, reportQueryTimeout)
	defer cancel()
	raw, err := queryValues(ctx, pool, c.SQL, c.Args)
	if err != nil {
		return Result{}, fmt.Errorf("run custom report %s: %w", src.Key, err)
	}
	rows, truncated := shapeRows(raw, c.Limit, c.TopN)

	totalsRaw, err := queryValues(ctx, pool, c.TotalsSQL, c.Args)
	if err != nil {
		return Result{}, fmt.Errorf("total custom report %s: %w", src.Key, err)
	}
	var first []any
	if len(totalsRaw) > 0 {
		first = totalsRaw[0]
	}
	count, totals := shapeTotals(first)
	return Result{Columns: c.Columns, Rows: rows, Totals: totals, RowCount: count, Truncated: truncated}, nil
}

func queryValues(ctx context.Context, pool *pgxpool.Pool, sql string, args []any) ([][]any, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		out = append(out, values)
	}
	return out, rows.Err()
}

// shapeRows drops the one extra row BuildQuery asks for and reports whether
// the report was cut short. A top-N cut is what the user asked for, so it is
// never "truncated". The result is never nil (it is JSON-encoded).
func shapeRows(raw [][]any, limit int, topN bool) ([][]any, bool) {
	if len(raw) > limit {
		return raw[:limit], !topN
	}
	if raw == nil {
		return [][]any{}, false
	}
	return raw, false
}

// shapeTotals splits the totals row into its leading COUNT(*) and the values
// aligned to the result columns.
func shapeTotals(row []any) (int, []any) {
	if len(row) == 0 {
		return 0, []any{}
	}
	count := 0
	if f, ok := row[0].(float64); ok {
		count = int(f)
	}
	return count, row[1:]
}

// crmStages are the status-list keys of the CRM sources; they read
// lkp_crm_status rather than lkp_record_status.
func isCRMStage(code string) bool {
	switch code {
	case "LEAD", "PROS", "CUST":
		return true
	}
	return false
}

// StatusOptions lists the status names a status filter can pick for a record
// type (or CRM stage) code, in workflow order.
func StatusOptions(ctx context.Context, pool *pgxpool.Pool, code string) ([]string, error) {
	query := `
		SELECT rs.record_status_name
		FROM lkp_record_status rs
		JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
		WHERE rt.record_type_code = $1 AND rs.record_status_is_active AND rs.record_status_deleted_at IS NULL
		ORDER BY rs.record_status_id`
	if isCRMStage(code) {
		query = `
			SELECT cs.crm_status_name
			FROM lkp_crm_status cs
			JOIN lkp_record_type rt ON rt.record_type_id = cs.crm_status_record_type
			WHERE rt.record_type_code = $1 AND cs.crm_status_is_active AND cs.crm_status_deleted_at IS NULL
			ORDER BY cs.crm_status_id`
	}
	rows, err := pool.Query(ctx, query, code)
	if err != nil {
		return nil, fmt.Errorf("status options for %s: %w", code, err)
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan status option: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
