package inventory

// unit_summary.go — the totals strip above the Inventory list: how much stone
// is in each state, for whatever the user is currently looking at.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// UnitStatusTotal is how many units sit in one status and their combined area.
type UnitStatusTotal struct {
	Count int     `json:"count"`
	Area  float64 `json:"area"`
}

// UnitSummaryGroup is the breakdown for one unit of measure.
//
// Areas are only ever added within a group. A tenant can hold SQFT and SQM stone
// side by side, and a total that mixed the two would be wrong by 10.76x with
// nothing to say so.
type UnitSummaryGroup struct {
	UnitCode string                     `json:"unitCode"`
	ByStatus map[string]UnitStatusTotal `json:"byStatus"`
	// RecoveredArea is how much of the consumed area came back as offcuts, so the
	// consumed figure can be read as "of which X returned to stock".
	RecoveredArea float64 `json:"recoveredArea"`
}

// UnitSummary is the per-unit-of-measure breakdown of a filtered set of units.
type UnitSummary struct {
	Groups []UnitSummaryGroup `json:"groups"`
}

// SummarizeUnits totals the units matching the request's filters and search by
// status, through the same whitelist and joins as SearchUnits. Sort, limit and
// cursor are ignored: a summary describes the whole matching set, not a page.
func SummarizeUnits(ctx context.Context, pool *pgxpool.Pool, req query.Request) (UnitSummary, error) {
	req.Sort, req.Limit, req.Cursor = nil, 0, ""
	built, err := query.Build(req, unitResolver{}, 1)
	if err != nil {
		return UnitSummary{}, err
	}
	where := "s.slab_deleted_at IS NULL"
	if built.Where != "" {
		where += " AND " + built.Where
	}
	q := `SELECT au.unit_code, s.slab_status, COUNT(*),
	             COALESCE(SUM(s.slab_area), 0), COALESCE(SUM(rec.recovered_area), 0)` +
		unitFilterFrom + unitRecoveredJoin +
		` WHERE ` + where +
		` GROUP BY au.unit_code, s.slab_status
		  ORDER BY au.unit_code, s.slab_status`

	rows, err := pool.Query(ctx, q, built.Args...)
	if err != nil {
		return UnitSummary{}, fmt.Errorf("summarize inventory units: %w", err)
	}
	defer rows.Close()

	out := UnitSummary{Groups: []UnitSummaryGroup{}}
	idx := map[string]int{}
	for rows.Next() {
		var (
			code, status string
			total        UnitStatusTotal
			recovered    float64
		)
		if err := rows.Scan(&code, &status, &total.Count, &total.Area, &recovered); err != nil {
			return UnitSummary{}, fmt.Errorf("scan inventory unit summary: %w", err)
		}
		i, ok := idx[code]
		if !ok {
			i = len(out.Groups)
			idx[code] = i
			out.Groups = append(out.Groups, UnitSummaryGroup{UnitCode: code, ByStatus: map[string]UnitStatusTotal{}})
		}
		total.Area = roundTo(total.Area, areaScale)
		out.Groups[i].ByStatus[status] = total
		out.Groups[i].RecoveredArea = roundTo(out.Groups[i].RecoveredArea+recovered, areaScale)
	}
	if err := rows.Err(); err != nil {
		return UnitSummary{}, fmt.Errorf("summarize inventory units: %w", err)
	}
	return out, nil
}
