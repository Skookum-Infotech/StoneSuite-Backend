package vendortransactions

import "time"

// scheduledDateCol is the payment's release date, a static column the shared
// union shape does not carry.
const scheduledDateCol = "vendor_payment_scheduled_date"

// buildScheduledTotalSQL assembles the scheduled outflow and count over the
// granted vendor payment source's SCHD payments.
func buildScheduledTotalSQL(pay granted, vendorID, employee int, today time.Time) (string, []any) {
	b := newBinder(vendorID, employee, today)
	branch := pay.branchSQL(b)
	sql := "SELECT COALESCE(SUM(amount), 0)::float8, COUNT(*)" +
		" FROM (" + branch + ") x WHERE status_code = '" + codeScheduled + "'"
	return sql, b.args
}

// buildScheduledListSQL assembles the scheduled payments ordered by release
// date then id. The row set comes from the same branch as the total; the date is
// joined back on the payment's uuid. A payment with no date sorts last.
func buildScheduledListSQL(pay granted, vendorID, employee int, today time.Time, limit int) (string, []any) {
	b := newBinder(vendorID, employee, today)
	branch := pay.branchSQL(b)
	lim := b.add(limit)
	sql := "SELECT x.id::text, x.number, v." + scheduledDateCol + ", x.amount" +
		" FROM (" + branch + ") x" +
		" JOIN " + pay.Table + " v ON v." + pay.Table + "_uuid = x.id" +
		" WHERE x.status_code = '" + codeScheduled + "'" +
		" ORDER BY v." + scheduledDateCol + " NULLS LAST, x.id LIMIT " + lim
	return sql, b.args
}
