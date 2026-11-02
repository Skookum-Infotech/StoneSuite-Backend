package vendortransactions

import "time"

// buildAgingSQL assembles the open-bill balances grouped by whole days past
// due (today minus due date; a bill with no due date counts as not yet due).
// It reuses the bill branch's is_open, so the buckets always sum to the
// summary's open balance. Bucketing itself is done in Go by bucketIndex.
func buildAgingSQL(bill granted, vendorID, employee int, today time.Time) (string, []any) {
	b := newBinder(vendorID, employee, today)
	branch := bill.branchSQL(b)
	sql := "SELECT COALESCE(" + b.today() + " - due_date, 0)::int AS days_past_due," +
		" COALESCE(SUM(balance), 0)::float8, COUNT(*)" +
		" FROM (" + branch + ") x WHERE is_open GROUP BY 1"
	return sql, b.args
}
