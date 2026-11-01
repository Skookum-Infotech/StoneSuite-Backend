package vendortransactions

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Aging bucket labels, in display order.
const (
	AgingCurrent = "current"
	Aging1To30   = "1-30"
	Aging31To60  = "31-60"
	Aging61Plus  = "61+"
)

// Upper edges (inclusive, in days past due) of the first three buckets.
const (
	agingEdge30 = 30
	agingEdge60 = 60
)

// agingLabels is the fixed bucket order; a response always has all of them.
var agingLabels = []string{AgingCurrent, Aging1To30, Aging31To60, Aging61Plus}

// AgingBucket is the open payables falling in one days-past-due band.
type AgingBucket struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
	Count  int     `json:"count"`
}

// Aging is the vendor's open bills aged by due date. "Open" is exactly the summary's
// definition (vendorBill().OpenWhere: APPV, PART or ODUE with a balance), so the
// buckets sum to Summary.OpenBalance. This is narrower than "not void, paid or
// draft": pending-approval (PAPV) bills are excluded from both. A bill is aged by
// its due date alone, so an ODUE-flagged bill can still sit in current, and one
// with no due date counts as current. Buckets is nil when the
// caller cannot read vendor bills.
type Aging struct {
	Buckets      []AgingBucket `json:"buckets"`
	Total        float64       `json:"total"`
	ScopeLimited bool          `json:"scopeLimited"`
	Denied       bool          `json:"-"` // vendor bills are unreadable to the caller
}

// bucketIndex maps whole days past due to an index into agingLabels. Zero or
// negative (due today or later) is current.
func bucketIndex(daysPastDue int) int {
	switch {
	case daysPastDue <= 0:
		return 0
	case daysPastDue <= agingEdge30:
		return 1
	case daysPastDue <= agingEdge60:
		return 2
	default:
		return 3
	}
}

// GetAging ages the vendor's open bills into four buckets using the same scope
// and is_open definition as the summary. Buckets is nil when bills are unreadable.
func GetAging(ctx context.Context, pool *pgxpool.Pool, identityID, vendorUUID string, today time.Time) (Aging, error) {
	sc, err := loadScope(ctx, pool, identityID, vendorUUID)
	if err != nil {
		return Aging{}, err
	}
	out := Aging{ScopeLimited: sc.limited()}
	bill, ok := sc.byKey(keyVendorBill)
	if !ok {
		out.Denied = true
		return out, nil
	}
	sql, args := buildAgingSQL(bill, sc.vendorID, sc.employee, today)
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return Aging{}, fmt.Errorf("age vendor payables: %w", err)
	}
	defer rows.Close()

	out.Buckets = make([]AgingBucket, len(agingLabels))
	for i, l := range agingLabels {
		out.Buckets[i].Label = l
	}
	for rows.Next() {
		var (
			days, n int
			amount  float64
		)
		if err := rows.Scan(&days, &amount, &n); err != nil {
			return Aging{}, fmt.Errorf("scan vendor aging: %w", err)
		}
		bk := &out.Buckets[bucketIndex(days)]
		bk.Amount += amount
		bk.Count += n
		out.Total += amount
	}
	if err := rows.Err(); err != nil {
		return Aging{}, fmt.Errorf("age vendor payables: %w", err)
	}
	return out, nil
}
